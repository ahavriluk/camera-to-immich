package main

import (
	"fmt"
	"os"

	"github.com/ohavrylyuk/camera-to-immich/internal/exif"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: test-aspect-ratio <file.ORF>")
		os.Exit(1)
	}

	for _, filePath := range os.Args[1:] {
		fmt.Printf("\n=== %s ===\n", filePath)
		
		info, err := exif.GetAspectRatio(filePath)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			continue
		}
		
		if info == nil {
			fmt.Println("No aspect ratio info found")
			continue
		}
		
		fmt.Printf("Aspect Ratio: %s\n", info.Ratio)
		fmt.Printf("Source Tag: %s\n", info.SourceTag)
		
		if info.CropFrame != nil {
			fmt.Printf("Crop Frame: X=%d, Y=%d, Width=%d, Height=%d\n", 
				info.CropFrame.X, info.CropFrame.Y, 
				info.CropFrame.Width, info.CropFrame.Height)
		}
	}
}