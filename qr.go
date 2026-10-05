// qr.go — QR-Code als Bitmap (einzige Stelle, die github.com/skip2/go-qrcode benötigt;
// `go mod tidy` in start.sh lädt die Abhängigkeit automatisch nach)
package main

import qrcode "github.com/skip2/go-qrcode"

// makeQR liefert das Bitmap inklusive Ruhezone (true = dunkles Modul); nil bei Fehler
func makeQR(content string) [][]bool {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return nil
	}
	return q.Bitmap()
}
