package tests

import (
	"github.com/Coder-is/TabForge/v3/gen"
	"github.com/Coder-is/TabForge/v3/helper"
	"os/exec"
	"strings"
)

func compileLauncher(launcherFile, configFile, tableFile string) ([]byte, error) {

	m := struct {
		ConfigFile string
	}{
		ConfigFile: strings.Replace(configFile, "\\", "\\\\", -1),
	}

	const textTemplate = `
package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
)

func main() {

	data, err := ioutil.ReadFile("{{.ConfigFile}}")
	if err != nil {
		fmt.Println(err)
		return
	}

	var config Table
	err = json.Unmarshal(data, &config)

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
		return
	}

	outData, err := json.MarshalIndent(&config, "", "\t")

	if err != nil {
		fmt.Println(err)
		os.Exit(1)
		return
	}

	fmt.Println(string(outData))
}
`
	data, err := gen.Render("launcher", textTemplate, m, nil)
	if err != nil {
		return nil, err
	}
	if err := helper.WriteFile(launcherFile, data); err != nil {
		return nil, err
	}

	cmd := exec.Command("go", "run", launcherFile, tableFile)

	output, err := cmd.CombinedOutput()

	if err != nil {
		return output, err
	}

	return output, nil
}
