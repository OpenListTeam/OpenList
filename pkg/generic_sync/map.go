package generic_sync

import (
	"sync"
)

type MapOf[K comparable, V any] struct {
	sync.Map
}

func (m *MapOf[K, V]) CompareAndDelete(key K, old V) (deleted bool) {
	return m.Map.CompareAndDelete(key, old)
}
func (m *MapOf[K, V]) CompareAndSwap(key K, old V, new V) (swapped bool) {
	return m.Map.CompareAndSwap(key, old, new)
}
func (m *MapOf[K, V]) Delete(key K) {
	m.Map.Delete(key)
}
func (m *MapOf[K, V]) Load(key K) (value V, ok bool) {
	v, ok := m.Map.Load(key)
	if ok {
		value = v.(V)
	}
	return value, ok
}
func (m *MapOf[K, V]) LoadAndDelete(key K) (value V, loaded bool) {
	v, loaded := m.Map.LoadAndDelete(key)
	if loaded {
		value = v.(V)
	}
	return value, loaded
}
func (m *MapOf[K, V]) LoadOrStore(key K, value V) (actual V, loaded bool) {
	a, loaded := m.Map.LoadOrStore(key, value)
	if loaded {
		actual = a.(V)
	}
	return actual, loaded
}
func (m *MapOf[K, V]) Range(f func(key K, value V) bool) {
	m.Map.Range(func(key, value any) bool {
		return f(key.(K), value.(V))
	})
}
func (m *MapOf[K, V]) Store(key K, value V) {
	m.Map.Store(key, value)
}
func (m *MapOf[K, V]) Swap(key K, value V) (previous V, loaded bool) {
	p, loaded := m.Map.Swap(key, value)
	if loaded {
		previous = p.(V)
	}
	return previous, loaded
}

func (m *MapOf[K, V]) Has(key K) bool {
	_, ok := m.Map.Load(key)
	return ok
}

func (m *MapOf[K, V]) Values() []V {
	var values []V
	m.Map.Range(func(_, value any) bool {
		values = append(values, value.(V))
		return true
	})
	return values
}

func (m *MapOf[K, V]) Count() int {
	count := 0
	m.Map.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func (m *MapOf[K, V]) Empty() bool {
	return m.Count() == 0
}

func (m *MapOf[K, V]) ToMap() map[K]V {
	ans := make(map[K]V)
	m.Range(func(key K, value V) bool {
		ans[key] = value
		return true
	})
	return ans
}

func (m *MapOf[K, V]) Clear() {
	m.Map.Clear()
}
