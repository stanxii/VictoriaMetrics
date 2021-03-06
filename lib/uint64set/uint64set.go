package uint64set

import (
	"math/bits"
	"sort"
	"unsafe"
)

// Set is a fast set for uint64.
//
// It should work faster than map[uint64]struct{} for semi-sparse uint64 values
// such as MetricIDs generated by lib/storage.
//
// It is unsafe calling Set methods from concurrent goroutines.
type Set struct {
	itemsCount int
	buckets    bucket32Sorter
}

type bucket32Sorter []bucket32

func (s *bucket32Sorter) Len() int { return len(*s) }
func (s *bucket32Sorter) Less(i, j int) bool {
	a := *s
	return a[i].hi < a[j].hi
}
func (s *bucket32Sorter) Swap(i, j int) {
	a := *s
	a[i], a[j] = a[j], a[i]
}

// Clone returns an independent copy of s.
func (s *Set) Clone() *Set {
	if s == nil {
		// Return an empty set, so data could be added into it later.
		return &Set{}
	}
	var dst Set
	dst.itemsCount = s.itemsCount
	if len(s.buckets) > 0 {
		dst.buckets = make([]bucket32, len(s.buckets))
		for i := range s.buckets {
			s.buckets[i].copyTo(&dst.buckets[i])
		}
	}
	return &dst
}

// SizeBytes returns an estimate size of s in RAM.
func (s *Set) SizeBytes() uint64 {
	if s == nil {
		return 0
	}
	n := uint64(unsafe.Sizeof(*s))
	for i := range s.buckets {
		b32 := &s.buckets[i]
		n += uint64(unsafe.Sizeof(b32))
		n += b32.sizeBytes()
	}
	return n
}

// Len returns the number of distinct uint64 values in s.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return s.itemsCount
}

// Add adds x to s.
func (s *Set) Add(x uint64) {
	hi := uint32(x >> 32)
	lo := uint32(x)
	for i := range s.buckets {
		b32 := &s.buckets[i]
		if b32.hi == hi {
			if b32.add(lo) {
				s.itemsCount++
			}
			return
		}
	}
	s.addAlloc(hi, lo)
}

func (s *Set) addAlloc(hi, lo uint32) {
	b32 := s.addBucket32()
	b32.hi = hi
	_ = b32.add(lo)
	s.itemsCount++
}

func (s *Set) addBucket32() *bucket32 {
	s.buckets = append(s.buckets, bucket32{})
	return &s.buckets[len(s.buckets)-1]
}

// Has verifies whether x exists in s.
func (s *Set) Has(x uint64) bool {
	if s == nil {
		return false
	}
	hi := uint32(x >> 32)
	lo := uint32(x)
	for i := range s.buckets {
		b32 := &s.buckets[i]
		if b32.hi == hi {
			return b32.has(lo)
		}
	}
	return false
}

// Del deletes x from s.
func (s *Set) Del(x uint64) {
	hi := uint32(x >> 32)
	lo := uint32(x)
	for i := range s.buckets {
		b32 := &s.buckets[i]
		if b32.hi == hi {
			if b32.del(lo) {
				s.itemsCount--
			}
			return
		}
	}
}

// AppendTo appends all the items from the set to dst and returns the result.
//
// The returned items are sorted.
//
// AppendTo can mutate s.
func (s *Set) AppendTo(dst []uint64) []uint64 {
	if s == nil {
		return dst
	}

	// pre-allocate memory for dst
	dstLen := len(dst)
	if n := s.Len() - cap(dst) + dstLen; n > 0 {
		dst = append(dst[:cap(dst)], make([]uint64, n)...)
		dst = dst[:dstLen]
	}
	// sort s.buckets if it isn't sorted yet
	if !sort.IsSorted(&s.buckets) {
		sort.Sort(&s.buckets)
	}
	for i := range s.buckets {
		dst = s.buckets[i].appendTo(dst)
	}
	return dst
}

// Union adds all the items from a to s.
func (s *Set) Union(a *Set) {
	// Clone a, since AppendTo may mutate it below.
	aCopy := a.Clone()
	if s.Len() == 0 {
		// Fast path if the initial set is empty.
		*s = *aCopy
		return
	}
	// TODO: optimize it
	for _, x := range aCopy.AppendTo(nil) {
		s.Add(x)
	}
}

// Intersect removes all the items missing in a from s.
func (s *Set) Intersect(a *Set) {
	if a.Len() == 0 {
		// Fast path
		*s = Set{}
		return
	}
	// TODO: optimize it
	for _, x := range s.AppendTo(nil) {
		if !a.Has(x) {
			s.Del(x)
		}
	}
}

// Subtract removes from s all the shared items between s and a.
func (s *Set) Subtract(a *Set) {
	if s.Len() == 0 {
		return
	}
	// Copy a because AppendTo below can mutate a.
	aCopy := a.Clone()
	// TODO: optimize it
	for _, x := range aCopy.AppendTo(nil) {
		s.Del(x)
	}
}

// Equal returns true if s contains the same items as a.
func (s *Set) Equal(a *Set) bool {
	if s.Len() != a.Len() {
		return false
	}
	// Copy a because AppendTo below can mutate a
	aCopy := a.Clone()
	// TODO: optimize it
	for _, x := range aCopy.AppendTo(nil) {
		if !s.Has(x) {
			return false
		}
	}
	return true
}

type bucket32 struct {
	hi      uint32
	b16his  []uint16
	buckets []bucket16
}

func (b *bucket32) sizeBytes() uint64 {
	n := uint64(unsafe.Sizeof(*b))
	n += 2 * uint64(len(b.b16his))
	for i := range b.buckets {
		b16 := &b.buckets[i]
		n += uint64(unsafe.Sizeof(b16))
		n += b16.sizeBytes()
	}
	return n
}

func (b *bucket32) copyTo(dst *bucket32) {
	dst.hi = b.hi
	dst.b16his = append(dst.b16his[:0], b.b16his...)
	// Do not reuse dst.buckets, since it may be used in other places.
	dst.buckets = nil
	if len(b.buckets) > 0 {
		dst.buckets = make([]bucket16, len(b.buckets))
		for i := range b.buckets {
			b.buckets[i].copyTo(&dst.buckets[i])
		}
	}
}

// This is for sort.Interface
func (b *bucket32) Len() int           { return len(b.b16his) }
func (b *bucket32) Less(i, j int) bool { return b.b16his[i] < b.b16his[j] }
func (b *bucket32) Swap(i, j int) {
	his := b.b16his
	buckets := b.buckets
	his[i], his[j] = his[j], his[i]
	buckets[i], buckets[j] = buckets[j], buckets[i]
}

const maxUnsortedBuckets = 32

func (b *bucket32) add(x uint32) bool {
	hi := uint16(x >> 16)
	lo := uint16(x)
	if len(b.buckets) > maxUnsortedBuckets {
		return b.addSlow(hi, lo)
	}
	for i, hi16 := range b.b16his {
		if hi16 == hi {
			return i < len(b.buckets) && b.buckets[i].add(lo)
		}
	}
	b.addAllocSmall(hi, lo)
	return true
}

func (b *bucket32) addAllocSmall(hi, lo uint16) {
	b.b16his = append(b.b16his, hi)
	b16 := b.addBucket16()
	_ = b16.add(lo)
	if len(b.buckets) > maxUnsortedBuckets {
		sort.Sort(b)
	}
}

func (b *bucket32) addBucket16() *bucket16 {
	b.buckets = append(b.buckets, bucket16{})
	return &b.buckets[len(b.buckets)-1]
}

func (b *bucket32) addSlow(hi, lo uint16) bool {
	n := binarySearch16(b.b16his, hi)
	if n < 0 || n >= len(b.b16his) || b.b16his[n] != hi {
		b.addAllocBig(hi, lo, n)
		return true
	}
	return n < len(b.buckets) && b.buckets[n].add(lo)
}

func (b *bucket32) addAllocBig(hi, lo uint16, n int) {
	if n < 0 {
		// This is a hint to Go compiler to remove automatic bounds checks below.
		return
	}
	if n >= len(b.b16his) {
		b.b16his = append(b.b16his, hi)
		b16 := b.addBucket16()
		_ = b16.add(lo)
		return
	}
	b.b16his = append(b.b16his[:n+1], b.b16his[n:]...)
	b.b16his[n] = hi
	b.buckets = append(b.buckets[:n+1], b.buckets[n:]...)
	b16 := &b.buckets[n]
	*b16 = bucket16{}
	_ = b16.add(lo)
}

func (b *bucket32) has(x uint32) bool {
	hi := uint16(x >> 16)
	lo := uint16(x)
	if len(b.buckets) > maxUnsortedBuckets {
		return b.hasSlow(hi, lo)
	}
	for i, hi16 := range b.b16his {
		if hi16 == hi {
			return i < len(b.buckets) && b.buckets[i].has(lo)
		}
	}
	return false
}

func (b *bucket32) hasSlow(hi, lo uint16) bool {
	n := binarySearch16(b.b16his, hi)
	if n < 0 || n >= len(b.b16his) || b.b16his[n] != hi {
		return false
	}
	return n < len(b.buckets) && b.buckets[n].has(lo)
}

func (b *bucket32) del(x uint32) bool {
	hi := uint16(x >> 16)
	lo := uint16(x)
	if len(b.buckets) > maxUnsortedBuckets {
		return b.delSlow(hi, lo)
	}
	for i, hi16 := range b.b16his {
		if hi16 == hi {
			return i < len(b.buckets) && b.buckets[i].del(lo)
		}
	}
	return false
}

func (b *bucket32) delSlow(hi, lo uint16) bool {
	n := binarySearch16(b.b16his, hi)
	if n < 0 || n >= len(b.b16his) || b.b16his[n] != hi {
		return false
	}
	return n < len(b.buckets) && b.buckets[n].del(lo)
}

func (b *bucket32) appendTo(dst []uint64) []uint64 {
	if len(b.buckets) <= maxUnsortedBuckets && !sort.IsSorted(b) {
		sort.Sort(b)
	}
	for i := range b.buckets {
		hi16 := b.b16his[i]
		dst = b.buckets[i].appendTo(dst, b.hi, hi16)
	}
	return dst
}

const (
	bitsPerBucket  = 1 << 16
	wordsPerBucket = bitsPerBucket / 64
)

type bucket16 struct {
	bits         *[wordsPerBucket]uint64
	smallPoolLen int
	smallPool    [56]uint16
}

func (b *bucket16) sizeBytes() uint64 {
	return uint64(unsafe.Sizeof(*b)) + uint64(unsafe.Sizeof(*b.bits))
}

func (b *bucket16) copyTo(dst *bucket16) {
	// Do not reuse dst.bits, since it may be used in other places.
	dst.bits = nil
	if b.bits != nil {
		bits := *b.bits
		dst.bits = &bits
	}
	dst.smallPoolLen = b.smallPoolLen
	dst.smallPool = b.smallPool
}

func (b *bucket16) add(x uint16) bool {
	if b.bits == nil {
		return b.addToSmallPool(x)
	}
	wordNum, bitMask := getWordNumBitMask(x)
	word := &b.bits[wordNum]
	ok := *word&bitMask == 0
	*word |= bitMask
	return ok
}

func (b *bucket16) addToSmallPool(x uint16) bool {
	if b.hasInSmallPool(x) {
		return false
	}
	if b.smallPoolLen < len(b.smallPool) {
		b.smallPool[b.smallPoolLen] = x
		b.smallPoolLen++
		return true
	}
	b.smallPoolLen = 0
	var bits [wordsPerBucket]uint64
	b.bits = &bits
	for _, v := range b.smallPool[:] {
		b.add(v)
	}
	b.add(x)
	return true
}

func (b *bucket16) has(x uint16) bool {
	if b.bits == nil {
		return b.hasInSmallPool(x)
	}
	wordNum, bitMask := getWordNumBitMask(x)
	return b.bits[wordNum]&bitMask != 0
}

func (b *bucket16) hasInSmallPool(x uint16) bool {
	for _, v := range b.smallPool[:b.smallPoolLen] {
		if v == x {
			return true
		}
	}
	return false
}

func (b *bucket16) del(x uint16) bool {
	if b.bits == nil {
		return b.delFromSmallPool(x)
	}
	wordNum, bitMask := getWordNumBitMask(x)
	word := &b.bits[wordNum]
	ok := *word&bitMask != 0
	*word &^= bitMask
	return ok
}

func (b *bucket16) delFromSmallPool(x uint16) bool {
	for i, v := range b.smallPool[:b.smallPoolLen] {
		if v == x {
			copy(b.smallPool[i:], b.smallPool[i+1:])
			b.smallPoolLen--
			return true
		}
	}
	return false
}

func (b *bucket16) appendTo(dst []uint64, hi uint32, hi16 uint16) []uint64 {
	hi64 := uint64(hi)<<32 | uint64(hi16)<<16
	if b.bits == nil {
		a := b.smallPool[:b.smallPoolLen]
		if len(a) > 1 {
			sort.Slice(a, func(i, j int) bool { return a[i] < a[j] })
		}
		for _, v := range a {
			x := hi64 | uint64(v)
			dst = append(dst, x)
		}
		return dst
	}
	var wordNum uint64
	for _, word := range b.bits {
		if word == 0 {
			wordNum++
			continue
		}
		x64 := hi64 | (wordNum * 64)
		for {
			tzn := uint64(bits.TrailingZeros64(word))
			if tzn >= 64 {
				break
			}
			word &^= uint64(1) << tzn
			x := x64 | tzn
			dst = append(dst, x)
		}
		wordNum++
	}
	return dst
}

func getWordNumBitMask(x uint16) (uint16, uint64) {
	wordNum := x / 64
	bitMask := uint64(1) << (x & 63)
	return wordNum, bitMask
}

func binarySearch16(u16 []uint16, x uint16) int {
	// The code has been adapted from sort.Search.
	n := len(u16)
	i, j := 0, n
	for i < j {
		h := int(uint(i+j) >> 1)
		if h >= 0 && h < len(u16) && u16[h] < x {
			i = h + 1
		} else {
			j = h
		}
	}
	return i
}
