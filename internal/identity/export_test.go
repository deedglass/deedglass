package identity

// Test hooks for the integration suites in package identity_test.

// SetFailNotify makes every deedglass_identity notification fail (nil restores it).
func SetFailNotify(s *Service, f func() error) { s.failNotify = f }

// SetAfterLoad pauses a cache load between its database read and its guarded Put (nil removes it).
func SetAfterLoad(c *Cache, f func()) { c.afterLoad = f }

// WatcherCount is the number of open Guard.Watch contexts.
func WatcherCount(k *KeyAuth) int { return k.watch.Len() }
