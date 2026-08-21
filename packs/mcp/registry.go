package mcp

import (
	"sync"

	"github.com/subosito/mow/ext"
)

var (
	registryMu    sync.Mutex
	transports    = map[string]*transportSlot{}
	orphanedByGen = map[int][]toolTransport{}
)

type transportSlot struct {
	name string
	gen  int
	tr   toolTransport
}

func init() {
	ext.RegisterGenerationRelease(releaseEngineGeneration)
}

// registerTransport replaces any prior transport for name. When the prior
// transport belongs to a BeforeNew generation that still has open Engines, it
// is kept alive until that generation is released (concurrent host engines).
func registerTransport(name string, gen int, tr toolTransport) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if old, ok := transports[name]; ok && old != nil && old.tr != nil && old.tr != tr {
		retireTransportLocked(old.gen, old.tr)
	}
	transports[name] = &transportSlot{name: name, gen: gen, tr: tr}
}

func retireTransportLocked(gen int, tr toolTransport) {
	if ext.GenerationEngineRefs(gen) > 0 {
		orphanedByGen[gen] = append(orphanedByGen[gen], tr)
		return
	}
	_ = tr.Close()
}

func unregisterTransport(name string) {
	registryMu.Lock()
	slot := transports[name]
	delete(transports, name)
	registryMu.Unlock()
	if slot != nil && slot.tr != nil {
		_ = slot.tr.Close()
	}
}

func releaseEngineGeneration(gen int) {
	if gen <= 0 {
		return
	}
	registryMu.Lock()
	var toClose []toolTransport
	for name, slot := range transports {
		if slot != nil && slot.gen == gen {
			toClose = append(toClose, slot.tr)
			delete(transports, name)
		}
	}
	if orph := orphanedByGen[gen]; len(orph) > 0 {
		toClose = append(toClose, orph...)
		delete(orphanedByGen, gen)
	}
	registryMu.Unlock()
	for _, tr := range toClose {
		_ = tr.Close()
	}
}

// registeredStdioPID returns the OS pid of the current stdio transport for
// name, or 0 if none. Identity is the pid captured at cmd.Start, not a
// side-channel marker the child writes later.
func registeredStdioPID(name string) int {
	registryMu.Lock()
	slot := transports[name]
	registryMu.Unlock()
	if slot == nil {
		return 0
	}
	rc, ok := slot.tr.(*reconnectingClient)
	if !ok {
		return 0
	}
	return rc.processPID()
}

func resetRegistryForTest() {
	registryMu.Lock()
	for name, slot := range transports {
		if slot != nil && slot.tr != nil {
			_ = slot.tr.Close()
		}
		delete(transports, name)
	}
	for gen, trs := range orphanedByGen {
		for _, tr := range trs {
			_ = tr.Close()
		}
		delete(orphanedByGen, gen)
	}
	registryMu.Unlock()
}
