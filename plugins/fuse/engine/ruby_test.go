package engine

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// rubyOracles are values Ruby 4.0.1 computed (record_goldens.rb, in the
// Fuse sandbox): what each Ruby behaviour the port reimplements answers.
type rubyOracles struct {
	Ruby   string  `json:"ruby"`
	Glibc  string  `json:"glibc"`
	Rounds [][]any `json:"rounds"` // [x, x.round(1), x.round(2), x.round(4), x.round(1).to_s, x.round(2).to_s]
	Sums   [][]any `json:"sums"`   // [[xs], xs.sum, xs.sum.round(2)]
	Sorts  [][]any `json:"sorts"`  // [[keys, nil = no position], the indices sort_by left them in]
	Mults  [][]any `json:"mults"`  // [amount, miles, hours, (amount*miles).round, (amount*[hours,1.0].max).round]
}

func loadOracles(t *testing.T) rubyOracles {
	t.Helper()
	f, err := os.Open("../testdata/austin/ruby_oracles.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var o rubyOracles
	if err := json.NewDecoder(gz).Decode(&o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestRubyRoundOracle(t *testing.T) {
	t.Parallel()
	o := loadOracles(t)
	bad := 0
	for _, r := range o.Rounds {
		x := r[0].(float64)
		for i, n := range []int{1, 2, 4} {
			if got, want := rubyRound(x, n), r[i+1].(float64); got != want {
				bad++
				t.Errorf("%v.round(%d) = %v, Ruby says %v", x, n, got, want)
			}
		}
		if got, want := rubyFloatString(rubyRound(x, 1)), r[4].(string); got != want {
			bad++
			t.Errorf("%v.round(1).to_s = %q, Ruby says %q", x, got, want)
		}
		if got, want := rubyFloatString(rubyRound(x, 2)), r[5].(string); got != want {
			bad++
			t.Errorf("%v.round(2).to_s = %q, Ruby says %q", x, got, want)
		}
		if bad > 20 {
			t.Fatal("too many")
		}
	}
}

func TestRubySumOracle(t *testing.T) {
	t.Parallel()
	for _, s := range loadOracles(t).Sums {
		var xs []float64
		for _, v := range s[0].([]any) {
			xs = append(xs, v.(float64))
		}
		if got, want := rubySum(xs), s[1].(float64); got != want {
			t.Errorf("%v.sum = %v, Ruby says %v", xs, got, want)
		}
		if got, want := rubyRound(rubySum(xs), 2), s[2].(float64); got != want {
			t.Errorf("%v.sum.round(2) = %v, Ruby says %v", xs, got, want)
		}
	}
}

// Ruby's sort_by leaves ties as glibc's qsort_r does; so must the port, or
// fees of equal position come out in another order than Fuse's.
func TestRubySortByOracle(t *testing.T) {
	t.Parallel()
	o := loadOracles(t)
	t.Logf("oracle: Ruby %s, %s", o.Ruby, o.Glibc)
	bad := 0
	for _, s := range o.Sorts {
		raw := s[0].([]any)
		type item struct {
			i   int
			key float64
		}
		items := make([]item, len(raw))
		for i, k := range raw {
			items[i] = item{i, inf}
			if k != nil {
				items[i].key = k.(float64)
			}
		}
		rubySortBy(items, func(it item) float64 { return it.key })
		got := make([]int, len(items))
		for i, it := range items {
			got[i] = it.i
		}
		var want []int
		for _, v := range s[1].([]any) {
			want = append(want, int(v.(float64)))
		}
		if !slices.Equal(got, want) {
			bad++
			if bad <= 5 {
				t.Errorf("keys %v: sorted %v, Ruby %v", raw, got, want)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d sorts differ from Ruby's", bad, len(o.Sorts))
	}
}

func TestRubyFloatMultiplyRoundOracle(t *testing.T) {
	t.Parallel()
	for _, m := range loadOracles(t).Mults {
		amount, miles, hours := m[0].(float64), m[1].(float64), m[2].(float64)
		if got, want := rubyRound(amount*miles, 0), m[3].(float64); got != want {
			t.Errorf("(%v * %v).round = %v, Ruby %v", amount, miles, got, want)
		}
		if got, want := rubyRound(amount*max(hours, 1.0), 0), m[4].(float64); got != want {
			t.Errorf("(%v * %v).round = %v, Ruby %v", amount, hours, got, want)
		}
	}
}

func TestRubyToI(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		in   any
		want int64
		ok   bool
	}{
		{nil, 0, true}, {float64(2), 2, true}, {2.9, 2, true}, {-2.9, -2, true}, {"2abc", 2, true}, {"abc", 0, true},
		{" 12 ", 12, true}, {"-3", -3, true}, {"1_000", 1000, true}, {"", 0, true}, {true, 0, false},
	} {
		got, ok := rubyToI(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%#v.to_i = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestFloorDiv(t *testing.T) {
	t.Parallel()
	for _, c := range [][3]int64{{7, 2, 3}, {-7, 2, -4}, {-6, 2, -3}, {0, 5, 0}, {-1, 100, -1}} {
		if got := floorDiv(c[0], c[1]); got != c[2] {
			t.Errorf("%d / %d = %d, Ruby %d", c[0], c[1], got, c[2])
		}
	}
}
