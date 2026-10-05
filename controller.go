// controller.go — liest gamepad-Eingaben via libevdev
// CGo bindet libevdev direkt ein, kein externes Binary nötig.
// Die Auswertung (Richtungen, Hysterese, Auto-Repeat) steckt in pad.go.

package main

/*
#cgo pkg-config: libevdev libudev
#include <libevdev/libevdev.h>
#include <libudev.h>
#include <fcntl.h>
#include <unistd.h>
#include <string.h>
#include <stdlib.h>

// Prüft ob ein Gerät ein Gamepad ist
static int is_gamepad(struct libevdev *dev) {
    if (libevdev_has_event_code(dev, EV_KEY, BTN_GAMEPAD))       return 1;
    if (libevdev_has_event_code(dev, EV_KEY, BTN_JOYSTICK))      return 1;
    if (libevdev_has_event_code(dev, EV_KEY, BTN_TRIGGER_HAPPY)) return 1;
    return 0;
}

// Schreibt alle Gamepad-Devnodes zeilenweise in buf, Rückgabe = Anzahl.
// Virtuelle uinput-Geräte (z.B. Steam Input) werden übersprungen, sonst käme
// jeder Tastendruck doppelt an. Bluetooth-Controller (uhid, /devices/virtual/misc/)
// bleiben erlaubt.
static int list_gamepads(char *buf, size_t size) {
    buf[0] = 0;
    struct udev *udev = udev_new();
    if (!udev) return 0;

    struct udev_enumerate *en = udev_enumerate_new(udev);
    udev_enumerate_add_match_subsystem(en, "input");
    udev_enumerate_scan_devices(en);

    int count = 0;
    struct udev_list_entry *entry;
    udev_list_entry_foreach(entry, udev_enumerate_get_list_entry(en)) {
        const char *syspath = udev_list_entry_get_name(entry);
        if (strstr(syspath, "/devices/virtual/input/")) continue;

        struct udev_device *dev = udev_device_new_from_syspath(udev, syspath);
        if (!dev) continue;

        const char *devnode = udev_device_get_devnode(dev);
        if (devnode && strstr(devnode, "/dev/input/event")) {
            int fd = open(devnode, O_RDONLY | O_NONBLOCK | O_CLOEXEC);
            if (fd >= 0) {
                struct libevdev *evdev = NULL;
                if (libevdev_new_from_fd(fd, &evdev) == 0) {
                    if (is_gamepad(evdev) && strlen(buf) + strlen(devnode) + 2 < size) {
                        strcat(buf, devnode);
                        strcat(buf, "\n");
                        count++;
                    }
                    libevdev_free(evdev);
                }
                close(fd);
            }
        }
        udev_device_unref(dev);
    }
    udev_enumerate_unref(en);
    udev_unref(udev);
    return count;
}
*/
import "C"
import (
	"log"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

var (
	startOnce sync.Once
	padsMu    sync.Mutex
	activePad = map[string]bool{} // Devnodes, die gerade gelesen werden
)

// startController startet die Controller-Erkennung genau EINMAL (egal wie oft aufgerufen).
func startController(ch chan string) {
	startOnce.Do(func() { go controllerLoop(ch) })
}

func scanGamepads() []string {
	buf := make([]byte, 4096)
	n := C.list_gamepads((*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	if n == 0 {
		return nil
	}
	s := strings.TrimSpace(C.GoString((*C.char)(unsafe.Pointer(&buf[0]))))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// controllerLoop sucht alle 2 s nach Controllern und startet für jeden NEUEN
// genau einen Leser. Getrennte Geräte verschwinden von selbst, neue werden erkannt.
func controllerLoop(ch chan<- string) {
	warned := false
	for {
		nodes := scanGamepads()
		if len(nodes) == 0 && !warned {
			log.Println("[SnowFox] Kein Controller gefunden, suche weiter…")
			warned = true
		}
		for _, node := range nodes {
			padsMu.Lock()
			busy := activePad[node]
			if !busy {
				activePad[node] = true
			}
			padsMu.Unlock()
			if busy {
				continue
			}
			warned = false
			go func(n string) {
				defer func() {
					padsMu.Lock()
					delete(activePad, n)
					padsMu.Unlock()
				}()
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[SnowFox] Controller-Fehler (%s): %v", n, r)
					}
				}()
				readController(n, ch)
			}(node)
		}
		time.Sleep(2 * time.Second)
	}
}

func readController(devnode string, ch chan<- string) {
	fd, err := unix.Open(devnode, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		log.Printf("[SnowFox] Kann %s nicht öffnen: %v", devnode, err)
		return
	}
	defer unix.Close(fd)

	var dev *C.struct_libevdev
	if rc := C.libevdev_new_from_fd(C.int(fd), &dev); rc < 0 {
		log.Printf("[SnowFox] libevdev-Fehler auf %s", devnode)
		return
	}
	defer C.libevdev_free(dev)

	log.Printf("[SnowFox] Controller verbunden: %s (%s)", C.GoString(C.libevdev_get_name(dev)), devnode)

	// Blockierenden Modus aktivieren
	flags, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags&^unix.O_NONBLOCK)

	// Wertebereich der Sticks (Xbox: -32768..32767, manche Pads: 0..255)
	minX := int32(C.libevdev_get_abs_minimum(dev, C.uint(0x00)))
	maxX := int32(C.libevdev_get_abs_maximum(dev, C.uint(0x00)))
	minY := int32(C.libevdev_get_abs_minimum(dev, C.uint(0x01)))
	maxY := int32(C.libevdev_get_abs_maximum(dev, C.uint(0x01)))
	// Trigger (ABS_Z = LT, ABS_RZ = RT): Wertebereich je nach Controller 0..255 oder 0..1023
	minLT := int32(C.libevdev_get_abs_minimum(dev, C.uint(0x02)))
	maxLT := int32(C.libevdev_get_abs_maximum(dev, C.uint(0x02)))
	minRT := int32(C.libevdev_get_abs_minimum(dev, C.uint(0x05)))
	maxRT := int32(C.libevdev_get_abs_maximum(dev, C.uint(0x05)))

	emit := func(cmd string) {
		select {
		case ch <- cmd:
		default:
			log.Printf("[SnowFox] Eingabe verworfen (Puffer voll): %s", cmd)
		}
	}
	pad := newPadState(emit)

	// Auto-Repeat per Timer, unabhängig von neuen Achsen-Ereignissen
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(10 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				pad.tick(now)
			}
		}
	}()

	var ev C.struct_input_event
	for {
		rc := C.libevdev_next_event(dev,
			C.LIBEVDEV_READ_FLAG_NORMAL|C.LIBEVDEV_READ_FLAG_BLOCKING,
			&ev)

		if rc == C.LIBEVDEV_READ_STATUS_SYNC {
			for rc == C.LIBEVDEV_READ_STATUS_SYNC {
				rc = C.libevdev_next_event(dev, C.LIBEVDEV_READ_FLAG_SYNC, &ev)
			}
			continue
		}
		if rc < 0 {
			break // Verbindung getrennt
		}
		if rc != C.LIBEVDEV_READ_STATUS_SUCCESS {
			continue
		}

		evType := uint16(ev._type)
		evCode := uint16(ev.code)
		evVal := int32(ev.value)
		now := time.Now()

		switch evType {
		case unix.EV_KEY:
			pad.button(evCode, evVal, now)
		case unix.EV_ABS:
			switch evCode {
			case 0x10, 0x11: // Steuerkreuz als Hat-Achse
				pad.setHat(evCode, evVal, now)
			case 0x00: // linker Stick X
				pad.setStick(0, normAxis(evVal, minX, maxX), now)
			case 0x01: // linker Stick Y
				pad.setStick(1, normAxis(evVal, minY, maxY), now)
			case 0x02: // LT
				pad.setTrigger(0, normTrigger(evVal, minLT, maxLT))
			case 0x05: // RT
				pad.setTrigger(1, normTrigger(evVal, minRT, maxRT))
			}
		}
	}

	pad.reset()
	log.Printf("[SnowFox] Controller getrennt: %s", devnode)
}
