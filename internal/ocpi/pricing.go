package ocpi

const DefaultCurrency = "sat"

var PricePerKwh = map[string]float64{
	"sat":  50,
	"msat": 0.05,
	"eur":  25,
	"usd":  27,
}

func PriceForUnit(unit string) float64 {
	if p, ok := PricePerKwh[unit]; ok {
		return p
	}
	return PricePerKwh[DefaultCurrency]
}

func MaxKwh(credit int, unit string) float64 {
	if credit <= 0 {
		return 0
	}
	return float64(credit) / PriceForUnit(unit)
}
