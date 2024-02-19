// SPDX-FileCopyrightText: 2024 George Stark <stark.georgy@gmail.com>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	libusb "github.com/gotmc/libusb/v2"
	getopt "github.com/pborman/getopt/v2"
	"golang.org/x/net/netutil"
)

const usbTimeout = 5000

var usbCtx *libusb.Context

type usbDevice struct {
	endpointIn  *libusb.EndpointDescriptor
	endpointOut *libusb.EndpointDescriptor
	device      *libusb.Device
	handle      *libusb.DeviceHandle
}

func showDeviceInfo(dev usbDevice) {

	deviceAddress, _ := dev.device.DeviceAddress()
	busNumber, _ := dev.device.BusNumber()
	usbDeviceDescriptor, _ := dev.device.DeviceDescriptor()

	log.Printf("found device %v:%v, vendor: %04x, product: %04x\n",
		busNumber,
		deviceAddress,
		usbDeviceDescriptor.VendorID,
		usbDeviceDescriptor.ProductID)
}

func usbDeviceOpen(serial string) (usbDevice, error) {

	var dev usbDevice
	devices, _ := usbCtx.DeviceList()
	deviceCount := 0
	for _, device := range devices {
		usbDeviceDescriptor, _ := device.DeviceDescriptor()

		configDescriptor, err := device.ActiveConfigDescriptor()
		if err != nil {
			//log.Printf("Failed getting the active config: %v", err)
			continue
		}
		if configDescriptor.NumInterfaces > 1 {
			//log.Printf("Too much interfaces: %v", configDescriptor.NumInterfaces)
			continue
		}

		ifaceDescriptor := configDescriptor.SupportedInterfaces[0].InterfaceDescriptors[0]
		if ifaceDescriptor.InterfaceClass != 0xff ||
			ifaceDescriptor.InterfaceSubClass != 0x42 ||
			ifaceDescriptor.InterfaceProtocol != 0x03 {
			continue
		}

		in := -1
		out := -1

		for i, endpoint := range ifaceDescriptor.EndpointDescriptors {
			if endpoint.TransferType() != libusb.BulkTransfer {
				continue
			}
			if endpoint.Direction() == 1 {
				in = i
			} else {
				out = i
			}
		}

		if in < 0 || out < 0 {
			continue
		}

		if serial != "" {
			handle, err := device.Open()
			if err != nil {
				//log.Printf("Error opening device: %v", err)
				continue
			}
			defer handle.Close()
			serialNumber, _ := handle.StringDescriptorASCII(usbDeviceDescriptor.SerialNumberIndex)
			if serialNumber != serial {
				continue
			}
		}
		dev.endpointIn = ifaceDescriptor.EndpointDescriptors[in]
		dev.endpointOut = ifaceDescriptor.EndpointDescriptors[out]
		dev.device = device
		showDeviceInfo(dev)
		deviceCount++
	}
	if deviceCount == 0 {
		return dev, fmt.Errorf("no apropriate usb device found")
	}
	if deviceCount > 1 {
		return dev, fmt.Errorf("found multiple devices")
	}

	var err error
	dev.handle, err = dev.device.Open()
	if err != nil {
		return dev, fmt.Errorf("open device failed: %v", err)
	}

	err = dev.handle.ClaimInterface(0)
	if err != nil {
		dev.handle.Close()
		return dev, fmt.Errorf("claime interface failed: %v", err)
	}
	return dev, nil
}

func usbDeviceClose(dev usbDevice) {

	dev.handle.ReleaseInterface(0)
	dev.handle.Close()
}

func main() {

	// TODO: add vid pid options
	argPort := getopt.StringLong("listen", 'l', ":5554", "<host>:port tcp host and port to listen to")
	argSerial := getopt.StringLong("serial", 's', "", "device serial number")
	argCheckDevice := getopt.BoolLong("check", 'c', "search fastboot device at start")
	argHelp := getopt.BoolLong("help", 'h', "print help")

	getopt.Parse()
	if *argHelp {
		getopt.PrintUsage(os.Stdout)
		os.Exit(0)
	}

	var err error
	usbCtx, err = libusb.NewContext()
	if err != nil {
		log.Fatalf("create USB context failed: %v", err)
	}
	defer usbCtx.Close()

	if *argCheckDevice {
		dev, err := usbDeviceOpen(*argSerial)
		if err != nil {
			log.Fatalf("error: %v", err)
		}
		usbDeviceClose(dev)
	}

	log.Printf("launching server at %v", *argPort)
	ln, err := net.Listen("tcp", *argPort)
	if err != nil {
		log.Fatalf("open tcp server failed: %v", err)
	}

	ln = netutil.LimitListener(ln, 1)

	for {
		var dev usbDevice
		var err error
		conn, _ := ln.Accept()
		if err = netReadHandshake(conn); err != nil {
			log.Printf("tcp: %v", err)
			continue
		}
		dev, err = usbDeviceOpen(*argSerial)
		if err != nil {
			log.Printf("device error: %v", err)
			time.Sleep(time.Second)
			conn.Close()
			continue
		}

		netWriteHandshake(conn)

		var response []byte = make([]byte, 256)
		for {
			data, err := netRead(conn)
			if err != nil {
				log.Printf("tcp: %v", err)
				break
			}
			if err = usbWrite(dev, data); err != nil {
				log.Printf("usb: %v", err)
				break
			}
			n, err := usbRead(dev, response)
			if err != nil {
				log.Printf("usb: %v", err)
				break
			}
			if err = netWrite(conn, response[0:n]); err != nil {
				log.Printf("tcp: %v", err)
				break
			}
		}
		conn.Close()
		usbDeviceClose(dev)
	}
}

func netReadHandshake(conn net.Conn) error {

	var header []byte = make([]byte, 4)
	reader := bufio.NewReader(conn)
	n, err := io.ReadFull(reader, header)
	if n != 4 || string(header) != "FB01" {
		return fmt.Errorf("read handshake header failed: %v", err)
	}
	return nil
}

func netWriteHandshake(conn net.Conn) error {

	_, err := conn.Write([]byte("FB01"))
	if err != nil {
		log.Printf("write handshake header failed: %v", err)
	}
	return err
}

func netRead(conn net.Conn) ([]byte, error) {

	reader := bufio.NewReader(conn)
	var header []byte = make([]byte, 8)
	if n, err := io.ReadFull(reader, header); n != 8 {
		return nil, fmt.Errorf("read header failed: %v", err)
	}

	size := binary.BigEndian.Uint64(header)

	var data []byte = make([]byte, size)
	if _, err := io.ReadFull(reader, data); err != nil {
		return nil, fmt.Errorf("read packet failed: %v", err)
	}

	return data, nil
}

func netWrite(conn net.Conn, data []byte) error {

	var header []byte = make([]byte, 8)
	binary.BigEndian.PutUint64(header, uint64(len(data)))
	if _, err := conn.Write(header); err != nil {
		return fmt.Errorf("write header failed: %v", err)
	}
	if n, err := conn.Write(data); err != nil || n != len(data) {
		return fmt.Errorf("write packet failed: %v %v", n, err)
	}
	return nil
}

func usbWrite(dev usbDevice, data []byte) error {

	endpoint := dev.endpointOut
	count := (len(data) + int(endpoint.MaxPacketSize) - 1) / int(endpoint.MaxPacketSize)
	log.Printf("usb send: %v, %v %v\n", len(data), count, endpoint.MaxPacketSize)

	offset := 0
	for i := 0; i < count; i++ {
		size := (len(data) - offset)
		if size > int(endpoint.MaxPacketSize) {
			size = int(endpoint.MaxPacketSize)
		}
		_, err := dev.handle.BulkTransfer(endpoint.EndpointAddress, data[offset:offset+size], size, usbTimeout)
		if err != nil {
			return fmt.Errorf("write failed: %v", err)
		}
		offset = offset + size
	}
	return nil
}

func usbRead(dev usbDevice, data []byte) (int, error) {

	n, err := dev.handle.BulkTransfer(dev.endpointIn.EndpointAddress, data, len(data), usbTimeout)
	if err != nil {
		return n, fmt.Errorf("read failed: %v", err)
	}
	return n, nil
}
