package integration

import (
	"context"
	"fmt"
	"time"

	codereadytoolchainv1alpha1 "github.com/codeready-toolchain/api/api/v1alpha1"
	"github.com/devfile/library/pkg/util"
	. "github.com/onsi/ginkgo/v2"
	appstudioApi "github.com/redhat-appstudio/application-api/api/v1alpha1"
	kubeCl "github.com/redhat-appstudio/e2e-tests/pkg/apis/kubernetes"
	"github.com/redhat-appstudio/e2e-tests/pkg/constants"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils"
	"github.com/redhat-appstudio/e2e-tests/pkg/utils/tekton"
	integrationv1alpha1 "github.com/redhat-appstudio/integration-service/api/v1alpha1"
	integrationv1beta1 "github.com/redhat-appstudio/integration-service/api/v1beta1"
	releasev1alpha1 "github.com/redhat-appstudio/release-service/api/v1alpha1"
	releasemetadata "github.com/redhat-appstudio/release-service/metadata"
	tektonv1beta1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"knative.dev/pkg/apis"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SuiteController struct {
	*kubeCl.CustomClient
}

func NewSuiteController(kube *kubeCl.CustomClient) (*SuiteController, error) {
	return &SuiteController{
		kube,
	}, nil
}

func (h *SuiteController) HaveTestsSucceeded(snapshot *appstudioApi.Snapshot) bool {
	return meta.IsStatusConditionTrue(snapshot.Status.Conditions, "HACBSTestSucceeded") ||
		meta.IsStatusConditionTrue(snapshot.Status.Conditions, "AppStudioTestSucceeded")
}

func (h *SuiteController) HaveTestsFinished(snapshot *appstudioApi.Snapshot) bool {
	return meta.FindStatusCondition(snapshot.Status.Conditions, "HACBSTestSucceeded") != nil ||
		meta.FindStatusCondition(snapshot.Status.Conditions, "AppStudioTestSucceeded") != nil
}

func (h *SuiteController) MarkTestsSucceeded(snapshot *appstudioApi.Snapshot) (*appstudioApi.Snapshot, error) {
	patch := client.MergeFrom(snapshot.DeepCopy())
	meta.SetStatusCondition(&snapshot.Status.Conditions, metav1.Condition{
		Type:    "AppStudioTestSucceeded",
		Status:  metav1.ConditionTrue,
		Reason:  "Passed",
		Message: "Snapshot Passed",
	})
	err := h.KubeRest().Status().Patch(context.TODO(), snapshot, patch)
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// GetSnapshot returns the Snapshot in the namespace and nil if it's not found
// It will search for the Snapshot based on the Snapshot name, associated PipelineRun name or Component name
// In the case the List operation fails, an error will be returned.
func (h *SuiteController) GetSnapshot(snapshotName, pipelineRunName, componentName, namespace string) (*appstudioApi.Snapshot, error) {
	ctx := context.Background()
	// If Snapshot name is provided, try to get the resource directly
	if len(snapshotName) > 0 {
		snapshot := &appstudioApi.Snapshot{}
		if err := h.KubeRest().Get(ctx, types.NamespacedName{Name: snapshotName, Namespace: namespace}, snapshot); err != nil {
			return nil, fmt.Errorf("couldn't find Snapshot with name '%s' in '%s' namespace", snapshotName, namespace)
		}
		return snapshot, nil
	}
	// Search for the Snapshot in the namespace based on the associated Component or PipelineRun
	snapshots := &appstudioApi.SnapshotList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}
	err := h.KubeRest().List(ctx, snapshots, opts...)
	if err != nil {
		return nil, fmt.Errorf("error when listing Snapshots in '%s' namespace", namespace)
	}
	for _, snapshot := range snapshots.Items {
		if snapshot.Name == snapshotName {
			return &snapshot, nil
		}
		// find snapshot by pipelinerun name
		if len(pipelineRunName) > 0 && snapshot.Labels["appstudio.openshift.io/build-pipelinerun"] == pipelineRunName {
			return &snapshot, nil

		}
		// find snapshot by component name
		if len(componentName) > 0 && snapshot.Labels["appstudio.openshift.io/component"] == componentName {
			return &snapshot, nil

		}
	}
	return nil, fmt.Errorf("no snapshot found for component '%s', pipelineRun '%s' in '%s' namespace", componentName, pipelineRunName, namespace)
}

func (h *SuiteController) GetReleasesWithSnapshot(snapshot *appstudioApi.Snapshot, namespace string) ([]releasev1alpha1.Release, error) {
	releases := &releasev1alpha1.ReleaseList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}

	err := h.KubeRest().List(context.TODO(), releases, opts...)
	if err != nil {
		return nil, err
	}

	for _, release := range releases.Items {
		GinkgoWriter.Printf("Release %s is found\n", release.Name)
	}

	return releases.Items, nil
}

// Get return the status from the Application Custom Resource object
func (h *SuiteController) GetIntegrationTestScenarios(applicationName, namespace string) (*[]integrationv1beta1.IntegrationTestScenario, error) {
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}

	integrationTestScenarioList := &integrationv1beta1.IntegrationTestScenarioList{}
	err := h.KubeRest().List(context.TODO(), integrationTestScenarioList, opts...)
	if err != nil {
		return nil, err
	}

	items := make([]integrationv1beta1.IntegrationTestScenario, 0)
	for _, t := range integrationTestScenarioList.Items {
		if t.Spec.Application == applicationName {
			items = append(items, t)
		}
	}
	return &items, nil
}

func (h *SuiteController) CreateSnapshotWithImage(applicationName, namespace, componentName, containerImage string) (*appstudioApi.Snapshot, error) {
	hasSnapshot := &appstudioApi.Snapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "snapshot-sample-" + util.GenerateRandomString(4),
			Namespace: namespace,
			Labels: map[string]string{
				"test.appstudio.openshift.io/type":           "component",
				"appstudio.openshift.io/component":           componentName,
				"pac.test.appstudio.openshift.io/event-type": "push",
			},
		},
		Spec: appstudioApi.SnapshotSpec{
			Application: applicationName,
			Components: []appstudioApi.SnapshotComponent{
				{
					Name:           componentName,
					ContainerImage: containerImage,
				},
			},
		},
	}
	err := h.KubeRest().Create(context.TODO(), hasSnapshot)
	if err != nil {
		return nil, err
	}
	return hasSnapshot, err
}

func (h *SuiteController) DeleteSnapshot(hasSnapshot *appstudioApi.Snapshot, namespace string) error {
	err := h.KubeRest().Delete(context.TODO(), hasSnapshot)
	return err
}

func (h *SuiteController) DeleteIntegrationTestScenario(testScenario *integrationv1beta1.IntegrationTestScenario, namespace string) error {
	err := h.KubeRest().Delete(context.TODO(), testScenario)
	return err
}

func (h *SuiteController) CreateReleasePlan(applicationName, namespace string) (*releasev1alpha1.ReleasePlan, error) {
	testReleasePlan := &releasev1alpha1.ReleasePlan{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "test-releaseplan-",
			Namespace:    namespace,
			Labels: map[string]string{
				releasemetadata.AutoReleaseLabel: "true",
				releasemetadata.AttributionLabel: "true",
			},
		},
		Spec: releasev1alpha1.ReleasePlanSpec{
			Application: applicationName,
			Target:      "default",
		},
	}
	err := h.KubeRest().Create(context.TODO(), testReleasePlan)
	if err != nil {
		return nil, err
	}

	return testReleasePlan, err
}

func (h *SuiteController) CreateIntegrationPipelineRun(snapshotName, namespace, componentName, integrationTestScenarioName string) (*tektonv1beta1.PipelineRun, error) {
	testpipelineRun := &tektonv1beta1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "component-pipelinerun" + "-",
			Namespace:    namespace,
			Labels: map[string]string{
				"pipelinesascode.tekton.dev/event-type": "push",
				"appstudio.openshift.io/component":      componentName,
				"pipelines.appstudio.openshift.io/type": "test",
				"appstudio.openshift.io/snapshot":       snapshotName,
				"test.appstudio.openshift.io/scenario":  integrationTestScenarioName,
			},
		},
		Spec: tektonv1beta1.PipelineRunSpec{
			PipelineRef: &tektonv1beta1.PipelineRef{
				Name:   "integration-pipeline-pass",
				Bundle: "quay.io/redhat-appstudio/example-tekton-bundle:integration-pipeline-pass",
			},
			Params: []tektonv1beta1.Param{
				{
					Name: "output-image",
					Value: tektonv1beta1.ArrayOrString{
						Type:      "string",
						StringVal: "quay.io/redhat-appstudio/sample-image",
					},
				},
			},
		},
	}
	err := h.KubeRest().Create(context.TODO(), testpipelineRun)
	if err != nil {
		return nil, err
	}
	return testpipelineRun, err
}

func (h *SuiteController) CreateIntegrationTestScenario(applicationName, namespace, bundleURL, pipelineName string) (*integrationv1alpha1.IntegrationTestScenario, error) {
	integrationTestScenario := &integrationv1alpha1.IntegrationTestScenario{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-pass-" + util.GenerateRandomString(4),
			Namespace: namespace,
			Labels: map[string]string{
				"test.appstudio.openshift.io/optional": "false",
			},
		},
		Spec: integrationv1alpha1.IntegrationTestScenarioSpec{
			Application: applicationName,
			Bundle:      bundleURL,
			Pipeline:    pipelineName,
		},
	}

	err := h.KubeRest().Create(context.TODO(), integrationTestScenario)
	if err != nil {
		return nil, err
	}
	return integrationTestScenario, nil
}

func (h *SuiteController) CreateIntegrationTestScenarioWithEnvironment(applicationName, namespace, bundleURL, pipelineName, environmentName string) (*integrationv1alpha1.IntegrationTestScenario, error) {
	integrationTestScenario := &integrationv1alpha1.IntegrationTestScenario{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-pass-" + util.GenerateRandomString(4),
			Namespace: namespace,
			Labels: map[string]string{
				"test.appstudio.openshift.io/optional": "false",
			},
		},
		Spec: integrationv1alpha1.IntegrationTestScenarioSpec{
			Application: applicationName,
			Bundle:      bundleURL,
			Pipeline:    pipelineName,
			Environment: integrationv1alpha1.TestEnvironment{
				Name: environmentName,
				Type: "POC",
			},
		},
	}

	err := h.KubeRest().Create(context.TODO(), integrationTestScenario)
	if err != nil {
		return nil, err
	}
	return integrationTestScenario, nil
}

func (h *SuiteController) WaitForIntegrationPipelineToBeFinished(testScenario *integrationv1beta1.IntegrationTestScenario, snapshot *appstudioApi.Snapshot, appNamespace string) error {
	return wait.PollImmediate(constants.PipelineRunPollingInterval, 20*time.Minute, func() (done bool, err error) {
		pipelineRun, err := h.GetIntegrationPipelineRun(testScenario.Name, snapshot.Name, appNamespace)
		if err != nil {
			GinkgoWriter.Println("PipelineRun has not been created yet for test scenario %s and snapshot %s/%s", testScenario.GetName(), snapshot.GetNamespace(), snapshot.GetName())
			return false, nil
		}
		for _, condition := range pipelineRun.Status.Conditions {
			GinkgoWriter.Printf("PipelineRun %s reason: %s\n", pipelineRun.Name, condition.Reason)

			if !pipelineRun.IsDone() {
				return false, nil
			}

			if pipelineRun.GetStatusCondition().GetCondition(apis.ConditionSucceeded).IsTrue() {
				return true, nil
			} else {
				return false, fmt.Errorf(tekton.GetFailedPipelineRunLogs(h.KubeRest(), h.KubeInterface(), pipelineRun))
			}
		}
		return false, nil
	})
}

// GetComponentPipeline returns the pipeline for a given component labels
func (h *SuiteController) GetBuildPipelineRun(componentName, applicationName, namespace string, pacBuild bool, sha string) (*tektonv1beta1.PipelineRun, error) {
	pipelineRunLabels := map[string]string{"appstudio.openshift.io/component": componentName, "appstudio.openshift.io/application": applicationName, "pipelines.appstudio.openshift.io/type": "build"}
	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{
			"pipelines.appstudio.openshift.io/type": "build",
			"appstudio.openshift.io/application":    applicationName,
			"appstudio.openshift.io/component":      componentName,
		},
	}

	if sha != "" {
		pipelineRunLabels["pipelinesascode.tekton.dev/sha"] = sha
	}

	list := &tektonv1beta1.PipelineRunList{}
	err := h.KubeRest().List(context.TODO(), list, opts...)

	if err != nil && !k8sErrors.IsNotFound(err) {
		return nil, fmt.Errorf("error listing pipelineruns in %s namespace: %v", namespace, err)
	}

	if len(list.Items) > 0 {
		return &list.Items[0], nil
	}

	return &tektonv1beta1.PipelineRun{}, fmt.Errorf("no pipelinerun found for component %s %s", componentName, utils.GetAdditionalInfo(applicationName, namespace))
}

// GetComponentPipeline returns the pipeline for a given component labels
func (h *SuiteController) GetIntegrationPipelineRun(integrationTestScenarioName string, snapshotName string, namespace string) (*tektonv1beta1.PipelineRun, error) {

	opts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{
			"pipelines.appstudio.openshift.io/type": "test",
			"test.appstudio.openshift.io/scenario":  integrationTestScenarioName,
			"appstudio.openshift.io/snapshot":       snapshotName,
		},
	}

	list := &tektonv1beta1.PipelineRunList{}
	err := h.KubeRest().List(context.TODO(), list, opts...)

	if err != nil && !k8sErrors.IsNotFound(err) {
		return nil, fmt.Errorf("error listing pipelineruns in %s namespace", namespace)
	}

	if len(list.Items) > 0 {
		return &list.Items[0], nil
	}

	return &tektonv1beta1.PipelineRun{}, fmt.Errorf("no pipelinerun found for integrationTestScenario %s (snapshot: %s, namespace: %s)", integrationTestScenarioName, snapshotName, namespace)
}

// GetComponentPipeline returns the pipeline for a given component labels
func (h *SuiteController) GetSnapshotEnvironmentBinding(applicationName string, namespace string, environment *appstudioApi.Environment) (*appstudioApi.SnapshotEnvironmentBinding, error) {
	snapshotEnvironmentBindingList := &appstudioApi.SnapshotEnvironmentBindingList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}

	err := h.KubeRest().List(context.TODO(), snapshotEnvironmentBindingList, opts...)
	if err != nil {
		return nil, err
	}

	for _, binding := range snapshotEnvironmentBindingList.Items {
		if binding.Spec.Application == applicationName && binding.Spec.Environment == environment.Name {
			return &binding, nil
		}
	}

	return nil, fmt.Errorf("no SnapshotEnvironmentBinding found in environment %s %s", environment.Name, utils.GetAdditionalInfo(applicationName, namespace))
}

func (h *SuiteController) GetSpaceRequests(namespace string) (*codereadytoolchainv1alpha1.SpaceRequestList, error) {
	spaceRequestList := &codereadytoolchainv1alpha1.SpaceRequestList{}

	opts := []client.ListOption{
		client.InNamespace(namespace),
	}

	err := h.KubeRest().List(context.Background(), spaceRequestList, opts...)
	if err != nil && !k8sErrors.IsNotFound(err) {
		return nil, fmt.Errorf("error occurred while trying to list spaceRequests in %s namespace: %v", namespace, err)
	}

	return spaceRequestList, nil
}

func (h *SuiteController) CreateIntegrationTestScenario_beta1(applicationName, namespace, gitURL, revision, pathInRepo string) (*integrationv1beta1.IntegrationTestScenario, error) {
	integrationTestScenario := &integrationv1beta1.IntegrationTestScenario{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-resolver-pass-" + util.GenerateRandomString(4),
			Namespace: namespace,
			Labels: map[string]string{
				"test.appstudio.openshift.io/optional": "false",
			},
		},
		Spec: integrationv1beta1.IntegrationTestScenarioSpec{
			Application: applicationName,
			ResolverRef: integrationv1beta1.ResolverRef{
				Resolver: "git",
				Params: []integrationv1beta1.ResolverParameter{
					{
						Name:  "url",
						Value: gitURL,
					},
					{
						Name:  "revision",
						Value: revision,
					},
					{
						Name:  "pathInRepo",
						Value: pathInRepo,
					},
				},
			},
		},
	}

	err := h.KubeRest().Create(context.TODO(), integrationTestScenario)
	if err != nil {
		return nil, err
	}
	return integrationTestScenario, nil
}

// CreateSnapshot creates a Snapshot using the given parameters.
func (h *SuiteController) CreateSnapshotWithComponents(name string, namespace string, applicationName string, snapshotComponents []appstudioApi.SnapshotComponent) (*appstudioApi.Snapshot, error) {
	snapshot := &appstudioApi.Snapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: appstudioApi.SnapshotSpec{
			Application: applicationName,
			Components:  snapshotComponents,
		},
	}
	return snapshot, h.KubeRest().Create(context.TODO(), snapshot)
}

// GetSnapshotByComponent returns the first snapshot in namespace if exist, else will return nil
func (h *SuiteController) GetSnapshotByComponent(namespace string) (*appstudioApi.Snapshot, error) {
	snapshot := &appstudioApi.SnapshotList{}
	opts := []client.ListOption{
		client.MatchingLabels{
			"test.appstudio.openshift.io/type": "component",
		},
		client.InNamespace(namespace),
	}
	err := h.KubeRest().List(context.TODO(), snapshot, opts...)

	if err == nil && len(snapshot.Items) > 0 {
		return &snapshot.Items[0], nil
	}
	return nil, err
}

// DeleteAllSnapshotsInASpecificNamespace removes all snapshots from a specific namespace. Useful when creating a lot of resources and want to remove all of them
func (h *SuiteController) DeleteAllSnapshotsInASpecificNamespace(namespace string, timeout time.Duration) error {
	if err := h.KubeRest().DeleteAllOf(context.TODO(), &appstudioApi.Snapshot{}, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("error deleting snapshots from the namespace %s: %+v", namespace, err)
	}

	snapshotList := &appstudioApi.SnapshotList{}
	return utils.WaitUntil(func() (done bool, err error) {
		if err := h.KubeRest().List(context.Background(), snapshotList, &client.ListOptions{Namespace: namespace}); err != nil {
			return false, nil
		}
		return len(snapshotList.Items) == 0, nil
	}, timeout)
}
