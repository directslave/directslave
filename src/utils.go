package main

import (
	"math"
	"net/url"
)

func UrlEncode(str string) (string, error) {
	u, err := url.Parse(str)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func ClientIP(addr string) (ip string) {
	ip = hostOfAddr(addr)
	if DEMO == 1 {
		ip = "DEMO IP ***.***.***.***"
	}
	return
}

func Round(val float64, roundOn float64, places int) (newVal float64) {
	var round float64
	pow := math.Pow(10, float64(places))
	digit := pow * val
	_, div := math.Modf(digit)
	if div >= roundOn {
		round = math.Ceil(digit)
	} else {
		round = math.Floor(digit)
	}
	newVal = round / pow
	return
}
