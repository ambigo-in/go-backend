package ride

func DecodePolyline(encoded string) [][2]float64 {
	if encoded == "" {
		return nil
	}
	var points [][2]float64
	index := 0
	lat := 0
	lng := 0

	for index < len(encoded) {
		var result, shift int
		var b byte
		for {
			b = encoded[index] - 63
			index++
			result |= int(b&0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		dlat := result >> 1
		if result&1 != 0 {
			dlat = ^dlat
		}
		lat += dlat

		result, shift = 0, 0
		for {
			b = encoded[index] - 63
			index++
			result |= int(b&0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		dlng := result >> 1
		if result&1 != 0 {
			dlng = ^dlng
		}
		lng += dlng

		points = append(points, [2]float64{float64(lat) / 1e5, float64(lng) / 1e5})
	}
	return points
}
