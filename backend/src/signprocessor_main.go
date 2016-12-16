package main

import (
	"signprocessor"

	"time"
)

func main() {
	for ; ; time.Sleep(120 * time.Second) {
		signprocessor.EventLoop()
	}
}
