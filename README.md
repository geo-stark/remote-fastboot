# remote-fastboot
remote-fastboot is a tool intended for updating usb-connected fastboot devices over network.

Native fastboot client can interract with devices thru usb and tcp but many devices support
only usb interface. Remote-fastboot emulates tcp fastboot protocol at one end
and forward fastboot commands to device over usb at other end.

### Local test
./remote-fastboot -l :5444
./fastboot -s tcp:127.0.0.1:5444 flash system system.img

### Command-line options:
-l - host and port to listen to
-s - device serial number (if several devices are connected simulaneously)
-c - check if device is descovrable before starting the server

### Dependencies:
libusb-1.0
