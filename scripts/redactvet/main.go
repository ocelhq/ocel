package main

import "golang.org/x/tools/go/analysis/unitchecker"

func main() { unitchecker.Main(Analyzer) }
