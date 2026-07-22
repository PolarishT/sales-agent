package main

import (
	"net"
	"testing"
)

func TestOpenListenerReturnsErrorWhenAddressIsInUse(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用测试端口: %v", err)
	}
	defer occupied.Close()

	listener, err := openListener(occupied.Addr().String())
	if err == nil {
		listener.Close()
		t.Fatal("openListener() error = nil, want an error")
	}
}
