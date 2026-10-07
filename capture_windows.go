//go:build windows

package main

import (
	"flag"
	"fmt"
	"os/exec"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
)

func main() {
	flag.Parse()
	loadItemNames()
	loadRecipes()
	app := NewApp("prices.json")

	fmt.Println("Albion market sniffer")
	fmt.Println("Only reads your own game traffic. It never changes or controls the game.")
	fmt.Println("Keep this window open. Close it to stop.")
	fmt.Println()

	addr, err := app.serve(*port)
	if err != nil {
		fmt.Println("Couldn't start the window:", err)
		return
	}
	fmt.Println("Window:", addr)
	exec.Command("rundll32", "url.dll,FileProtocolHandler", addr).Start()

	startCapture(app)
	select {} // keep running until the console is closed
}

func startCapture(app *App) {
	devices, err := pcap.FindAllDevs()
	if err != nil || len(devices) == 0 {
		app.event("warn", "Npcap isn't installed. Get it from npcap.com, tick \"WinPcap API-compatible mode\", then restart this.")
		return
	}

	photon := NewPhoton()
	photon.OnMessage = app.handle
	photon.OnEncrypted = app.onEncrypted

	// Every adapter sends its packets into one channel, so only one
	// goroutine ever touches the Photon decoder.
	payloads := make(chan Packet, 1000)
	opened := 0
	for _, d := range devices {
		handle, err := pcap.OpenLive(d.Name, 65535, false, pcap.BlockForever)
		if err != nil {
			continue
		}
		if err := handle.SetBPFFilter(fmt.Sprintf("udp port %d", gamePort)); err != nil {
			handle.Close()
			continue
		}
		opened++
		go capture(handle, payloads)
	}

	app.mu.Lock()
	app.adapters = opened
	app.mu.Unlock()

	if opened == 0 {
		app.event("warn", "Couldn't open any network adapters. Run as Administrator, or reinstall Npcap without \"Restrict to Administrators only\".")
		return
	}
	app.event("zone", "Listening on %d network adapters. Start Albion and open a market.", opened)

	go func() {
		for p := range payloads {
			app.packet()
			app.recordPacket(p)
			photon.Feed(p.Data)
		}
	}()
}

func capture(handle *pcap.Handle, out chan<- Packet) {
	src := gopacket.NewPacketSource(handle, handle.LinkType())
	for packet := range src.Packets() {
		if udp, ok := packet.Layer(layers.LayerTypeUDP).(*layers.UDP); ok && len(udp.Payload) > 0 {
			out <- Packet{Data: udp.Payload, FromServer: udp.SrcPort == gamePort}
		}
	}
}
