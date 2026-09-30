package main

import "fmt"

func hasImportSpace(available, expanded uint64) bool {
	return available >= expanded && available-expanded >= importDiskHeadroom
}

func ensureImportSpace(path string, expanded uint64) error {
	available, err := availableImportSpace(path)
	if err != nil {
		return err
	}
	if !hasImportSpace(available, expanded) {
		return fmt.Errorf("not enough disk space to import package")
	}
	return nil
}
