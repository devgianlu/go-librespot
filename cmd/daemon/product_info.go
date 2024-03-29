package main

import (
	"encoding/xml"
	"strings"
)

type ProductInfo struct {
	XMLName  xml.Name `xml:"products"`
	Products []struct {
		XMLName      xml.Name `xml:"product"`
		Type         string   `xml:"type"`
		HeadFilesUrl string   `xml:"head-files-url"`
		ImageUrl     string   `xml:"image-url"`
		Autoplay     string   `xml:"autoplay"`
	} `xml:"product"`
}

func (pi ProductInfo) ImageUrl(fileId string) string {
	return strings.Replace(pi.Products[0].ImageUrl, "{file_id}", strings.ToLower(fileId), 1)
}

func (pi ProductInfo) AutoplayEnabled() bool {
	return pi.Products[0].Autoplay == "1"
}
