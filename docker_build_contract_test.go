package buildcontract

import (
	"os"
	"strings"
	"testing"
)

const (
	sdkRuntimePath = "/SDK/zatca-einvoicing-sdk-238-R4.0.0"
	sdkContext     = "zatca-sdk"
	sdkEnvContract = "${ZATCA_SDK_PATH:?ZATCA_SDK_PATH must point to the private ZATCA SDK root}"
)

func TestDaemonDockerBuildContract(t *testing.T) {
	dockerfile := readContractFile(t, "Dockerfile")
	for _, required := range []string{
		"# syntax=docker/dockerfile:1",
		"COPY --from=" + sdkContext + " . " + sdkRuntimePath,
		"RUN test -f " + sdkRuntimePath + "/Apps/fatoora",
		"WORKDIR " + sdkRuntimePath,
		"ENV FATOORA_HOME=" + sdkRuntimePath + "/Apps",
		"ENV SDK_CONFIG=" + sdkRuntimePath + "/Configuration/config.json",
		"ENTRYPOINT [\"daemon\"]",
		"VOLUME [\"/data/zatca-xml\"]",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile is missing required build/runtime contract %q", required)
		}
	}
	if strings.Contains(dockerfile, "COPY zatca-einvoicing-sdk-238-R4.0.0 ") {
		t.Error("Dockerfile still copies the SDK from the implicit repository-relative directory")
	}

	for _, composeFile := range []string{"docker-compose.yml", "docker-compose.test.yml"} {
		t.Run(composeFile, func(t *testing.T) {
			compose := readContractFile(t, composeFile)
			if !strings.Contains(compose, "additional_contexts:") {
				t.Fatalf("%s does not declare additional build contexts", composeFile)
			}
			if !strings.Contains(compose, sdkContext+": "+sdkEnvContract) {
				t.Errorf("%s does not require the explicit %s build input", composeFile, sdkEnvContract)
			}
		})
	}

	readme := readContractFile(t, "README.md")
	for _, required := range []string{
		"--build-context " + sdkContext + "=",
		"ZATCA_SDK_PATH",
		"Apps/fatoora",
	} {
		if !strings.Contains(readme, required) {
			t.Errorf("README is missing SDK build guidance %q", required)
		}
	}

	envExample := readContractFile(t, ".env.example")
	if !strings.Contains(envExample, "ZATCA_SDK_PATH=") {
		t.Error(".env.example does not document the required private SDK path")
	}

	gitignore := readContractFile(t, ".gitignore")
	if !strings.Contains(gitignore, "zatca-einvoicing-sdk-*/") {
		t.Error(".gitignore no longer protects the proprietary SDK directory")
	}
}

func readContractFile(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
