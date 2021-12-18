package e2e

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

var (
	binDir     string
	failedTest bool

	testNamespace string = "storage-capacity-prioritization-scheduler-test"

	//go:embed testdata/pod-pvc-template.yaml
	podPVCTemplateYAML string

	podPVCTemplateOnce sync.Once
	podPVCTmpl         *template.Template
)

func execAtLocal(cmd string, input []byte, args ...string) ([]byte, []byte, error) {
	var stdout, stderr bytes.Buffer
	command := exec.Command(cmd, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr

	if len(input) != 0 {
		command.Stdin = bytes.NewReader(input)
	}

	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func kubectl(args ...string) ([]byte, []byte, error) {
	return execAtLocal(filepath.Join(binDir, "kubectl"), nil, args...)
}

func kubectlWithInput(input []byte, args ...string) ([]byte, []byte, error) {
	return execAtLocal(filepath.Join(binDir, "kubectl"), input, args...)
}

func TestMtest(t *testing.T) {
	if os.Getenv("E2ETEST") == "" {
		t.Skip("Run under e2e/")
		return
	}
	rand.Seed(time.Now().UnixNano())

	RegisterFailHandler(Fail)

	SetDefaultEventuallyPollingInterval(time.Second)
	SetDefaultEventuallyTimeout(5 * time.Minute)

	RunSpecs(t, "Test on sanity")
}

func createNamespace(ns string) {
	stdout, stderr, err := kubectl("create", "namespace", ns)
	Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
	Eventually(func() error {
		return waitCreatingDefaultSA(ns)
	}).Should(Succeed())
}

func waitCreatingDefaultSA(ns string) error {
	stdout, stderr, err := kubectl("get", "sa", "-n", ns, "default")
	if err != nil {
		return fmt.Errorf("default sa is not found. stdout=%s, stderr=%s, err=%v", stdout, stderr, err)
	}
	return nil
}

var _ = BeforeSuite(func() {
	By("[BeforeSuite] Getting the directory path which contains some binaries")
	binDir = os.Getenv("BINDIR")
	Expect(binDir).ShouldNot(BeEmpty())
	fmt.Println("This test uses the binaries under " + binDir)

	By("[BeforeSuite] Waiting for storage-capacity-prioritization-scheduler to get ready")
	Eventually(func() error {
		stdout, stderr, err := kubectl("-n", "storage-capacity-prioritization-scheduler", "get", "deploy", "storage-capacity-prioritization-scheduler", "-o", "json")
		if err != nil {
			return errors.New(string(stderr))
		}

		var deploy appsv1.Deployment
		err = yaml.Unmarshal(stdout, &deploy)
		if err != nil {
			return err
		}

		if deploy.Status.AvailableReplicas != 1 {
			return errors.New("storage-capacity-prioritization-scheduler is not available yet")
		}

		return nil
	}).Should(Succeed())

	By("[BeforeSuite] Creating namespace for test")
	createNamespace(testNamespace)
})

var _ = AfterSuite(func() {
	if !failedTest {
		By("[AfterSuite] Delete namespace for autoresizer tests")
		stdout, stderr, err := kubectl("delete", "namespace", testNamespace)
		Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
	}
})

type resource struct {
	resource string
	name     string
}

var _ = Describe("storage_capacity_prioritization", func() {
	var resources []resource

	var _ = AfterEach(func() {
		By("[AfterEach] cleanup resources")
		for _, r := range resources {
			stdout, stderr, err := kubectl("-n", testNamespace, "delete", r.resource, r.name)
			Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s", stdout, stderr)
		}
	})

	It("should create a Pod with a PVC", func() {
		pvcName := "test-pvc"
		sc := "topolvm-provisioner"
		mode := string(corev1.PersistentVolumeFilesystem)
		request := "1Gi"
		limit := "2Gi"
		resources = createPodPVC(resources, pvcName, sc, mode, pvcName, request, limit)
	})
})

func createPodPVC(resources []resource, pvcName, storageClassName, volumeMode, podName, request, limit string) []resource {
	By("create a PVC and a pod for test")
	podPVCYAML, err := buildPodPVCTemplateYAML(pvcName, storageClassName, volumeMode, pvcName, request, limit)
	Expect(err).ShouldNot(HaveOccurred())
	stdout, stderr, err := kubectlWithInput(podPVCYAML, "apply", "-f", "-")
	Expect(err).ShouldNot(HaveOccurred(), "stdout=%s, stderr=%s yaml=\n%s", stdout, stderr, podPVCYAML)
	resources = append(resources, resource{resource: "pod", name: pvcName})
	resources = append(resources, resource{resource: "pvc", name: pvcName})

	By("waiting for creating the volume and running the pod")
	Eventually(func() error {
		stdout, stderr, err := kubectl("get", "-n", testNamespace, "pod", pvcName, "-o", "yaml")
		if err != nil {
			return fmt.Errorf("failed to get pod name of %s/%s. stdout: %s, stderr: %s, err: %v", testNamespace, pvcName, stdout, stderr, err)
		}

		var pod corev1.Pod
		err = yaml.Unmarshal(stdout, &pod)
		if err != nil {
			return err
		}

		if pod.Status.Phase != corev1.PodRunning {
			return errors.New("Pod is not running")
		}

		return nil
	}).Should(Succeed())

	return resources
}

func buildPodPVCTemplateYAML(pvcName, storageClassName, volumeMode, podName, request, limit string) ([]byte, error) {
	var b bytes.Buffer
	var err error

	podPVCTemplateOnce.Do(func() {
		podPVCTmpl, err = template.New("").Parse(podPVCTemplateYAML)
	})
	if err != nil {
		return b.Bytes(), err
	}

	params := map[string]string{
		"pvcName":          pvcName,
		"storageClassName": storageClassName,
		"volumeMode":       volumeMode,
		"podName":          podName,
		"namespace":        testNamespace,
		"resourceRequest":  request,
		"resourceLimit":    limit,
	}
	err = podPVCTmpl.Execute(&b, params)
	return b.Bytes(), err
}
