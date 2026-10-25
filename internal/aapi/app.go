package aapi

const AppVersion = "32.19.0.100"
const AppBuild = "1243250306"
const UserAgent = "Amazon.com/32.19.0.100 (Android/14/Pixel 8)"
const Marketplace = "AE08WJ6YKNBMC"
const ProductPath = "/api/marketplaces/" + Marketplace + "/products/"

func AppKey() Key {
	return Key{ID: "6b1c8f11a75391923a7", Secret: []byte{72, 243, 113, 36, 49, 14, 248, 2, 206, 203, 67, 206, 14, 175, 27, 116, 91, 117, 117, 183, 140, 185, 19, 40, 173, 16, 55, 171, 43, 228, 18, 114}}
}
