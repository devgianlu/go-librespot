package main

import (
	"encoding/hex"
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
	} `xml:"product"`
}

func (pi ProductInfo) ImageUrl(fileId []byte) *string {
	if len(pi.Products) == 0 || pi.Products[0].ImageUrl == "" {
		return nil
	} else if len(fileId) == 0 {
		return nil
	}

	fileIdHex := strings.ToLower(hex.EncodeToString(fileId))
	val := strings.Replace(pi.Products[0].ImageUrl, "{file_id}", fileIdHex, 1)
	return &val
}
