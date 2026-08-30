package panel

import "sync"

// keyedMutex serializes work for one key without retaining every key forever.
// A small reference count keeps the entry alive while callers are waiting and
// removes it after the last holder releases the lock.
type keyedMutex[K comparable] struct {
	mu    sync.Mutex
	items map[K]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func newKeyedMutex[K comparable]() *keyedMutex[K] {
	return &keyedMutex[K]{items: make(map[K]*keyedMutexEntry)}
}

func (k *keyedMutex[K]) lock(key K) func() {
	k.mu.Lock()
	if k.items == nil {
		k.items = make(map[K]*keyedMutexEntry)
	}
	entry := k.items[key]
	if entry == nil {
		entry = &keyedMutexEntry{}
		k.items[key] = entry
	}
	entry.refs++
	k.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 && k.items[key] == entry {
			delete(k.items, key)
		}
		k.mu.Unlock()
	}
}

func (k *keyedMutex[K]) size() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.items)
}
