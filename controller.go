// controller.go — liest alle Gamepad-Inputs via libevdev
// CGo bindet libevdev direkt ein, kein externes Binary nötig.

package main

/*
#cgo pkg-config: libevdev libudev
#include <libevdev/libevdev.h>
#include <libudev.h>
#include <fcntl.h>
#include <unistd.h>
#include <string.h>
#include <stdlib.h>
#include <errno.h>

// Prüft ob ein Gerät ein Gamepad ist
int is_gamepad(struct libevdev *dev) {
    if (libevdev_has_event_code(dev, EV_KEY, BTN_GAMEPAD))   return 1;
    if (libevdev_has_event_code(dev, EV_KEY, BTN_JOYSTICK))  return 1;
    if (libevdev_has_event_code(dev, EV_KEY, BTN_TRIGGER_HAPPY)) return 1;
    return 0;
}

// Gibt Devnode-String zurück oder NULL — Caller muss free() aufrufen
char *find_gamepads(int index) {
    struct udev *udev = udev_new();
    if (!udev) return NULL;

    struct udev_enumerate *en = udev_enumerate_new(udev);
    udev_enumerate_add_match_subsystem(en, "input");
    udev_enumerate_scan_devices(en);

    struct udev_list_entry *devices = udev_enumerate_get_list_entry(en);
    struct udev_list_entry *entry;
    int count = 0;
    char *result = NULL;

    udev_list_entry_foreach(entry, devices) {
        const char *syspath = udev_list_entry_get_name(entry);
        struct udev_device *dev = udev_device_new_from_syspath(udev, syspath);
        if (!dev) continue;

        const char *devnode = udev_device_get_devnode(dev);
        if (!devnode || !strstr(devnode, "/dev/input/event")) {
            udev_device_unref(dev);
            continue;
        }

        // Capability prüfen
        int fd = open(devnode, O_RDONLY | O_NONBLOCK);
        if (fd >= 0) {
            struct libevdev *evdev = NULL;
            if (libevdev_new_from_fd(fd, &evdev) == 0) {
                if (is_gamepad(evdev)) {
                    if (count == index) {
                        result = strdup(devnode);
                        libevdev_free(evdev);
                        close(fd);
                        udev_device_unref(dev);
                        break;
                    }
                    count++;
                }
                libevdev_free(evdev);
            }
            close(fd);
        }
        udev_device_unref(dev);
    }

    udev_enumerate_unref(en);
    udev_unref(udev);
    return result;
}
*/
import "C"
import (
	"log"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func startController(ch chan string) {
	go controllerLoop(ch)
}

func controllerLoop(ch chan<- string) {
	for {
		// Alle Gamepad-Devices sammeln
		var devnodes []string
		for i := 0; ; i++ {
			cstr := C.find_gamepads(C.int(i))
			if cstr == nil {
				break
			}
			devnodes = append(devnodes, C.GoString(cstr))
			C.free(unsafe.Pointer(cstr))
		}

		if len(devnodes) == 0 {
			log.Println("[SnowFox] Kein Controller gefunden, warte 3s...")
			time.Sleep(3 * time.Second)
			continue
		}

		// Für jeden Controller eine Goroutine
		done := make(chan struct{}, len(devnodes))
		for _, node := range devnodes {
			go func(devnode string) {
				defer func() { done <- struct{}{} }()
				readController(devnode, ch)
			}(node)
		}

		// Warten bis mindestens ein Controller sich trennt, dann neu scannen
		<-done
		time.Sleep(500 * time.Millisecond)
	}
}

func readController(devnode string, ch chan<- string) {
    log.Printf("[SnowFox] Controller verbunden: %s", devnode)

    fd, err := unix.Open(devnode, unix.O_RDONLY, 0)
    if err != nil {
        log.Printf("[SnowFox] Kann %s nicht öffnen: %v", devnode, err)
        return
    }
    defer unix.Close(fd)

    var dev *C.struct_libevdev
    if rc := C.libevdev_new_from_fd(C.int(fd), &dev); rc < 0 {
        log.Printf("[SnowFox] libevdev Fehler auf %s", devnode)
        return
    }
    defer C.libevdev_free(dev)

    name := C.GoString(C.libevdev_get_name(dev))
    log.Printf("[SnowFox] Verbunden: \"%s\"", name)

    // Blockierenden Modus aktivieren
    flags, _ := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
    unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags&^unix.O_NONBLOCK)

    var ev C.struct_input_event

    // Präzises Tracking pro Analog-Achse für flüssiges Menü-Scrolling
    lastStickCmds := make(map[uint16]string)
    lastStickTimes := make(map[uint16]time.Time)

    const (
        threshPress   = 22000                  // Aktivierungsschwelle (Stick weit genug gedrückt)
        threshRelease = 12000                  // Deaktivierungsschwelle (Hysterese gegen Zittern)
        initialDelay  = 350 * time.Millisecond // Pause nach dem ersten Klick (verhindert Überspringen)
        repeatDelay   = 130 * time.Millisecond // Scroll-Geschwindigkeit beim Festhalten
    )

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

        // Digitale Buttons — feuern exakt einmal bei Druck (evVal == 1)
        if evType == unix.EV_KEY && evVal == 1 {
            cmd := buttonToCmd(evCode)
            if cmd != "" {
                ch <- cmd
            }
        }

        // Hat-Switch / Steuerkreuz (Digitaler ABS-Typ, sendet nur bei Änderung, kein Jitter)
        if evType == unix.EV_ABS && (evCode == 0x10 || evCode == 0x11) {
            cmd := absToCmd(evCode, evVal)
            if cmd != "" {
                ch <- cmd
            }
        }

        // Analog-Sticks (ABS_X: 0x00 / ABS_Y: 0x01) — Jetzt mit Hysterese & Auto-Repeat
        if evType == unix.EV_ABS && (evCode == 0x00 || evCode == 0x01) {
            lastCmd := lastStickCmds[evCode]
            cmd := ""

            // Auswertung mit intelligentem Schwellenkorridor
            if evCode == 0x00 { // X-Achse (Links / Rechts)
                if lastCmd == "LEFT" {
                    if evVal < -threshRelease { cmd = "LEFT" }
                } else if lastCmd == "RIGHT" {
                    if evVal > threshRelease { cmd = "RIGHT" }
                } else {
                    if evVal < -threshPress { cmd = "LEFT" }
                    if evVal > threshPress { cmd = "RIGHT" }
                }
            } else { // Y-Achse (Oben / Unten)
                if lastCmd == "UP" {
                    if evVal < -threshRelease { cmd = "UP" }
                } else if lastCmd == "DOWN" {
                    if evVal > threshRelease { cmd = "DOWN" }
                } else {
                    if evVal < -threshPress { cmd = "UP" }
                    if evVal > threshPress { cmd = "DOWN" }
                }
            }

            now := time.Now()
            if cmd != "" {
                if cmd != lastCmd {
                    // Neu gedrückt oder Richtung direkt gewechselt -> Sofort feuern
                    ch <- cmd
                    lastStickCmds[evCode] = cmd
                    lastStickTimes[evCode] = now
                } else {
                    // Richtung wird gehalten -> Überprüfe intelligenten Auto-Repeat
                    if now.Sub(lastStickTimes[evCode]) >= initialDelay {
                        ch <- cmd
                        // Setzt den Timer mathematisch so zurück, dass Folgeschritte im schnelleren repeatDelay kommen
                        lastStickTimes[evCode] = now.Add(-initialDelay + repeatDelay)
                    }
                }
            } else {
                // Stick ist stabil in der neutralen Zone angekommen -> Reset für diese Achse
                lastStickCmds[evCode] = ""
            }
        }
    }

    log.Printf("[SnowFox] Controller getrennt: %s", devnode)
}

func buttonToCmd(code uint16) string {
	switch code {
	case 0x220: // BTN_DPAD_UP
		return "UP"
	case 0x221: // BTN_DPAD_DOWN
		return "DOWN"
	case 0x222: // BTN_DPAD_LEFT
		return "LEFT"
	case 0x223: // BTN_DPAD_RIGHT
		return "RIGHT"
	case 0x130: // BTN_A / BTN_EAST
		return "ENTER"
	case 0x131: // BTN_B / BTN_SOUTH
		return "BACK"
	case 0x136: // BTN_TL
		return "LB"
	case 0x137: // BTN_TR
		return "RB"
	case 0x13a: // BTN_SELECT
		return "BACK"
	case 0x13b: // BTN_START
		return "START"
	case 0x13c: // BTN_MODE (Xbox Guide / PS Logo)
		return "GUIDE"
	}
	return ""
}

func absToCmd(code uint16, val int32) string {
	const threshold = 16000
	switch code {
	case 0x10: // ABS_HAT0X
		if val == -1 {
			return "LEFT"
		}
		if val == 1 {
			return "RIGHT"
		}
	case 0x11: // ABS_HAT0Y
		if val == -1 {
			return "UP"
		}
		if val == 1 {
			return "DOWN"
		}
	case 0x00: // ABS_X
		if val < -threshold {
			return "LEFT"
		}
		if val > threshold {
			return "RIGHT"
		}
	case 0x01: // ABS_Y
		if val < -threshold {
			return "UP"
		}
		if val > threshold {
			return "DOWN"
		}
	}
	return ""
}