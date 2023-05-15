package integration

import (
	"context"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gexec"
	"github.com/onsi/gomega/types"
	av1alpha1 "github.com/openshift/addon-operator/apis/addons/v1alpha1"
	addoninstance "github.com/openshift/addon-operator/pkg/client"

	//"github.com/openshift/reference-addon/internal/controllers/addoninstance"
	internaltesting "github.com/openshift/reference-addon/internal/testing"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Status Controller", func() {
	var (
		ctx                  context.Context
		cancel               context.CancelFunc
		deleteLabel          string
		deleteLabelGen       = nameGenerator("ref-test-label")
		addonInstanceName    string
		addonInstanceNameGen = nameGenerator("ai-test-name")
		//addonInstanceNamespace string
		//addonInstanceNamespaceGen = nameGenerator("ai-test-namespace")
		namespace       string
		namespaceGen    = nameGenerator("ref-test-namespace")
		operatorName    string
		operatorNameGen = nameGenerator("ref-test-name")
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		addonInstanceName = addonInstanceNameGen()
		deleteLabel = deleteLabelGen()
		//addonInstanceNamespace = addonInstanceNamespaceGen()
		namespace = namespaceGen()
		operatorName = operatorNameGen()

		By("Starting manager")

		manager := exec.Command(_binPath,
			"-addon-instance-name", addonInstanceName,
			"-addon-instance-namespace", namespace,
			"-namespace", namespace,
			"-delete-label", deleteLabel,
			"-operator-name", operatorName,
			"-kubeconfig", _kubeConfigPath,
		)

		session, err := Start(manager, GinkgoWriter, GinkgoWriter)
		Expect(err).ToNot(HaveOccurred())

		By("Creating the addon namespace")

		ns := addonNamespace(namespace)
		//ains := addonNamespace(addonInstanceNamespace)
		addonInstance := addonInstanceObject(addonInstanceName, namespace)

		_client.Create(ctx, &ns)
		//_client.Create(ctx, &ains)
		_client.Create(ctx, &addonInstance)

		rbac, err := getRBAC(namespace, managerGroup)
		Expect(err).ToNot(HaveOccurred())

		for _, obj := range rbac {
			_client.Create(ctx, obj)
		}

		DeferCleanup(func() {
			cancel()

			By("Stopping the managers")

			session.Interrupt()

			if usingExistingCluster() {
				By("Deleting test namspace")

				_client.Delete(ctx, &ns)
			}
		})
	})

	When("Addon Instance Object Exists", func() {
		Context("Reference Addon Status Available'", func() {
			It("Addon Instance should report Availalbe condition", func() {
				addonInstance := addonInstanceObject(addonInstanceName, namespace)
				_client.EventuallyObjectExists(ctx, &addonInstance, internaltesting.WithTimeout(10*time.Second))

				expectedCondition := addoninstance.NewAddonInstanceConditionInstalled(
					"True",
					av1alpha1.AddonInstanceInstalledReasonSetupComplete,
					"All Components Available",
				)

				Eventually(func() []metav1.Condition {
					_client.Get(ctx, &addonInstance)

					return addonInstance.Status.Conditions
				}, 10*time.Second).Should(ContainElements(EqualCondition(expectedCondition)))
			})
		})
	})
})

func EqualCondition(expected metav1.Condition) types.GomegaMatcher {
	return And(
		HaveField("Type", expected.Type),
		HaveField("Status", expected.Status),
		HaveField("Reason", expected.Reason),
		HaveField("Message", expected.Message),
	)
}

//Tests needed

// Check Default of 10s for AddonInstanceReconciler
// Assert().Equal(10*time.Second, addonInstance.Spec.HeartbeatUpdatePeriod.Duration)
//	})
//})
// Default of 10s is hardcoded in AddonInstanceReconciler

//Does it trigger off no reference addon actions

//Does it trigger when reference addon does do something?

//Is addon instance unavilable during uninstall?
