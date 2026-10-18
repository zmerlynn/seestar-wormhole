package iotc

import "net/netip"

// Marker is the server-list local timeval, not a timestamp read off the wire.
// Seconds-only cache comparison is required by GCM key, IV and attempt cache contract.
type Marker struct {
	Seconds int64
	Micros  int32
}

// KeyRef is an attempt-scoped edge crypto handle, never an on-wire identifier.
type KeyRef uint64

// CacheEntry retains metadata and a reference to edge-owned AES key/IV material.
// Same-address ports and both directions deliberately share this vendor pair.
type CacheEntry struct {
	RequestedMarker      Marker
	Address              netip.Addr
	Public               [92]byte
	Marker, CachedMarker Marker
	Key                  KeyRef
	Pending              uint64
}

// Cache is per attempt, bounded to twelve server address strings. No LRU eviction.
// It must never be shared with DTLS or reused on reconnect.
type Cache struct {
	Entries [12]CacheEntry
	Count   int
}

// Install preserves unchanged markers; a change to byte 92 also updates metadata.
// All 92 metadata bytes affect installation, including the spare key-slot byte:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#gcm-key-iv-and-cache-contract
func (c *Cache) Install(s Server, at Marker) (int, error) {
	if !s.Address.IsValid() || !s.Address.Addr().Is4() || at.Seconds == 0 {
		return 0, ErrPacket
	}
	ip := s.Address.Addr()
	for i := 0; i < c.Count; i++ {
		e := &c.Entries[i]
		if e.Address == ip {
			if e.Public != s.Public {
				e.Public = s.Public
				e.Marker = at
			}
			return i, nil
		}
	}
	if c.Count == len(c.Entries) {
		return 0, ErrLimit
	}
	i := c.Count
	c.Count++
	c.Entries[i] = CacheEntry{Address: ip, Public: s.Public, Marker: at}
	return i, nil
}

// Lookup uses only the marker's seconds word, intentionally preserving the
// vendor's same-second old-pair behavior. A pending derivation is not usable.
// Vendor seconds-only cache behavior, separate from DTLS nonce rules:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#gcm-key-iv-and-cache-contract
func (c *Cache) Lookup(i int) (KeyRef, bool) {
	if i < 0 || i >= c.Count {
		return 0, false
	}
	e := c.Entries[i]
	return e.Key, e.Key != 0 && e.CachedMarker.Seconds == e.Marker.Seconds
}

// Clear removes references; the edge must destroy all attempt material on Dispose.
func (c *Cache) Clear() { *c = Cache{} }
