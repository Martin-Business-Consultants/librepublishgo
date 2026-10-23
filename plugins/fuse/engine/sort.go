package engine

import (
	"math"
	"math/bits"
)

// rubySortBy orders items as Ruby's sort_by { key } does, ties included,
// for numeric keys.
//
// QuoteFeeCalculator sorts its fees with sort_by { position || Float::INFINITY },
// and fees of equal position — every calculator line has Float::INFINITY —
// come out in whatever order the sort leaves them. With keys that are all
// Integers and Floats, MRI (3.3 on, Fuse runs 4.0.1) doesn't use ruby_qsort
// for sort_by: enum.c sorts the pairs itself with rb_uniform_sort, an
// introsort that is stable up to 16 elements (insertion sort) and not past
// that (a Hoare partition around a median of three). A stable sort would put
// tied lines of a long fee list in another order than Fuse does, so this is
// rb_uniform_sort step for step. TestRubySortByOracle checks it against
// 4,000 of Ruby's own sorts.
func rubySortBy[T any](items []T, key func(T) float64) {
	n := len(items)
	if n <= 1 {
		return
	}
	data := make([]uniformPair[T], n)
	for i, it := range items {
		data[i] = uniformPair[T]{key(it), it}
	}
	uniformSort(data)
	for i := range data {
		items[i] = data[i].v
	}
}

type uniformPair[T any] struct {
	k float64
	v T
}

// uniformSort is enum.c's rb_uniform_sort: nothing to do when already in
// order, else an introsort to depth 2*floor(log2(n)).
func uniformSort[T any](p []uniformPair[T]) {
	n := len(p)
	sorted := true
	for i := 1; i < n; i++ {
		if p[i-1].k > p[i].k {
			sorted = false
			break
		}
	}
	if sorted {
		return
	}
	d := bits.Len(uint(n)) - 1
	uniformIntro(p, 0, n, d<<1)
}

// uniformIntro is rb_uniform_quicksort_intro_2 over p[begin:end].
func uniformIntro[T any](p []uniformPair[T], begin, end, d int) {
	if end-begin <= 16 {
		uniformInsertion(p, begin, end)
		return
	}
	if d == 0 {
		uniformHeapsort(p, begin, end)
		return
	}
	x := med3(p[begin].k, p[begin+(end-begin)>>1].k, p[end-1].k)
	i, j := begin, end-1
	for {
		for p[i].k < x {
			i++
		}
		for x < p[j].k {
			j--
		}
		if i <= j {
			p[i], p[j] = p[j], p[i]
			i++
			j--
		}
		if i > j {
			break
		}
	}
	j++
	if end-j > 1 {
		uniformIntro(p, j, end, d-1)
	}
	if i-begin > 1 {
		uniformIntro(p, begin, i, d-1)
	}
}

// med3 is enum.c's med3_val.
func med3(a, b, c float64) float64 {
	if a < b {
		if b < c {
			return b
		}
		if c < a {
			return a
		}
		return c
	}
	if c < b {
		return b
	}
	if a < c {
		return a
	}
	return c
}

// uniformInsertion is rb_uniform_insertionsort_2: stable, an element moved
// down only past ones strictly greater.
func uniformInsertion[T any](p []uniformPair[T], begin, end int) {
	if end-begin < 2 {
		return
	}
	for idx := begin + 1; idx < end; idx++ {
		tmp := p[idx]
		j := idx
		if tmp.k < p[begin].k {
			for j > begin {
				p[j] = p[j-1]
				j--
			}
		} else {
			for tmp.k < p[j-1].k {
				p[j] = p[j-1]
				j--
			}
		}
		p[j] = tmp
	}
}

// uniformHeapsort is rb_uniform_heapsort_2, for the depth limit; a fee list
// never gets near it.
func uniformHeapsort[T any](p []uniformPair[T], begin, end int) {
	n := end - begin
	if n < 2 {
		return
	}
	q := p[begin:end]
	heapify := func(offset, last int) {
		tmp := q[offset]
		for {
			c := offset<<1 + 1
			if c > last {
				break
			}
			if c < last && q[c].k < q[c+1].k {
				c++
			}
			if !(tmp.k < q[c].k) {
				break
			}
			q[offset] = q[c]
			offset = c
		}
		q[offset] = tmp
	}
	for offset := n >> 1; offset > 0; {
		offset--
		heapify(offset, n-1)
	}
	for offset := n - 1; offset > 0; {
		q[0], q[offset] = q[offset], q[0]
		offset--
		heapify(0, offset)
	}
}

// inf is Ruby's Float::INFINITY, the position sort_by gives a fee with none.
var inf = math.Inf(1)
