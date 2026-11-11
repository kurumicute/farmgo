package news

import (
	"crypto/sha512"
	"math/big"
	"math/bits"
)

// pyRandom 與 CPython random.Random 相同的 Mersenne Twister（MT19937），只實作日報需要的部分：
// 以字串作種子（seed version 2：字串 + SHA-512 摘要轉成大整數）、random()、randrange(n)／choice()。
// 傳聞與措辭只由種子決定，必須和 Python 版產生完全相同的結果（Bot 切換語言版本後日報與挖寶仍一致）。
type pyRandom struct {
	mt  [624]uint32
	mti int
}

func newPyRandom(seed string) *pyRandom {
	b := []byte(seed)
	sum := sha512.Sum512(b)
	b = append(b, sum[:]...)
	n := new(big.Int).SetBytes(b) // int.from_bytes(..., "big")
	// init_by_array 的 key：大整數的 32 位元字組，最低位在前
	var key []uint32
	words := n.Bits()
	if bits.UintSize == 64 {
		for _, w := range words {
			key = append(key, uint32(w), uint32(uint64(w)>>32))
		}
	} else {
		for _, w := range words {
			key = append(key, uint32(w))
		}
	}
	for len(key) > 1 && key[len(key)-1] == 0 {
		key = key[:len(key)-1]
	}
	if len(key) == 0 {
		key = []uint32{0}
	}
	r := &pyRandom{}
	r.initByArray(key)
	return r
}

func (r *pyRandom) initGenrand(s uint32) {
	r.mt[0] = s
	for i := 1; i < 624; i++ {
		r.mt[i] = 1812433253*(r.mt[i-1]^(r.mt[i-1]>>30)) + uint32(i)
	}
	r.mti = 624
}

func (r *pyRandom) initByArray(key []uint32) {
	r.initGenrand(19650218)
	i, j := 1, 0
	k := max(624, len(key))
	for ; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1664525)) + key[j] + uint32(j)
		i++
		j++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
		if j >= len(key) {
			j = 0
		}
	}
	for k = 623; k > 0; k-- {
		r.mt[i] = (r.mt[i] ^ ((r.mt[i-1] ^ (r.mt[i-1] >> 30)) * 1566083941)) - uint32(i)
		i++
		if i >= 624 {
			r.mt[0] = r.mt[623]
			i = 1
		}
	}
	r.mt[0] = 0x80000000
}

func (r *pyRandom) genrand() uint32 {
	const upper, lower, matrixA = 0x80000000, 0x7fffffff, 0x9908b0df
	if r.mti >= 624 {
		var kk int
		for kk = 0; kk < 624-397; kk++ {
			y := (r.mt[kk] & upper) | (r.mt[kk+1] & lower)
			r.mt[kk] = r.mt[kk+397] ^ (y >> 1) ^ ((y & 1) * matrixA)
		}
		for ; kk < 623; kk++ {
			y := (r.mt[kk] & upper) | (r.mt[kk+1] & lower)
			r.mt[kk] = r.mt[kk+(397-624)] ^ (y >> 1) ^ ((y & 1) * matrixA)
		}
		y := (r.mt[623] & upper) | (r.mt[0] & lower)
		r.mt[623] = r.mt[396] ^ (y >> 1) ^ ((y & 1) * matrixA)
		r.mti = 0
	}
	y := r.mt[r.mti]
	r.mti++
	y ^= y >> 11
	y ^= (y << 7) & 0x9d2c5680
	y ^= (y << 15) & 0xefc60000
	y ^= y >> 18
	return y
}

// random 等同 random.random()：[0, 1) 的 53 位元浮點數。
func (r *pyRandom) random() float64 {
	a := r.genrand() >> 5
	b := r.genrand() >> 6
	return (float64(a)*67108864.0 + float64(b)) * (1.0 / 9007199254740992.0)
}

// randbelow 等同 randrange(n)（n 為 1 到 2^32 之間；_randbelow_with_getrandbits）。
func (r *pyRandom) randbelow(n int) int {
	if n <= 0 {
		return 0
	}
	k := bits.Len(uint(n))
	for {
		v := int(r.genrand() >> (32 - k))
		if v < n {
			return v
		}
	}
}

// choice 等同 random.choice(seq)。
func choice(r *pyRandom, seq []string) string {
	return seq[r.randbelow(len(seq))]
}
