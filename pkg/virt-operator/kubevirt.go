/*
 * This file is part of the KubeVirt project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2018 Red Hat, Inc.
 *
 */

package virt_operator

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	k8sv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	v1 "kubevirt.io/client-go/api/v1"
	"kubevirt.io/client-go/kubecli"
	"kubevirt.io/client-go/log"
	"kubevirt.io/kubevirt/pkg/controller"
	"kubevirt.io/kubevirt/pkg/virt-operator/creation/components"
	installstrategy "kubevirt.io/kubevirt/pkg/virt-operator/install-strategy"
	"kubevirt.io/kubevirt/pkg/virt-operator/util"
	operatorutil "kubevirt.io/kubevirt/pkg/virt-operator/util"
)

const (
	ConditionReasonDeploymentFailedExisting  = "ExistingDeployment"
	ConditionReasonDeploymentFailedError     = "DeploymentFailed"
	ConditionReasonDeletionFailedError       = "DeletionFailed"
	ConditionReasonUpdateNotImplementedError = "UpdatesNotImplemented"
	ConditionReasonDeploymentCreated         = "AllResourcesCreated"
	ConditionReasonDeploymentReady           = "AllComponentsReady"
	ConditionReasonUpdating                  = "UpdateInProgress"
)

type KubeVirtController struct {
	clientset            kubecli.KubevirtClient
	queue                workqueue.RateLimitingInterface
	kubeVirtInformer     cache.SharedIndexInformer
	recorder             record.EventRecorder
	stores               util.Stores
	informers            util.Informers
	kubeVirtExpectations util.Expectations
	installStrategyMutex sync.Mutex
	installStrategyMap   map[string]*installstrategy.InstallStrategy
	operatorNamespace    string
}

func NewKubeVirtController(
	clientset kubecli.KubevirtClient,
	informer cache.SharedIndexInformer,
	recorder record.EventRecorder,
	stores util.Stores,
	informers util.Informers,
	operatorNamespace string) *KubeVirtController {

	c := KubeVirtController{
		clientset:        clientset,
		queue:            workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter()),
		kubeVirtInformer: informer,
		recorder:         recorder,
		stores:           stores,
		informers:        informers,
		kubeVirtExpectations: util.Expectations{
			ServiceAccount:           controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("ServiceAccount")),
			ClusterRole:              controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("ClusterRole")),
			ClusterRoleBinding:       controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("ClusterRoleBinding")),
			Role:                     controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("Role")),
			RoleBinding:              controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("RoleBinding")),
			Crd:                      controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("Crd")),
			Service:                  controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("Service")),
			Deployment:               controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("Deployment")),
			DaemonSet:                controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("DaemonSet")),
			ValidationWebhook:        controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("ValidationWebhook")),
			InstallStrategyConfigMap: controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("ConfigMap")),
			InstallStrategyJob:       controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("Jobs")),
			PodDisruptionBudget:      controller.NewUIDTrackingControllerExpectations(controller.NewControllerExpectationsWithName("PodDisruptionBudgets")),
		},
		installStrategyMap: make(map[string]*installstrategy.InstallStrategy),
		operatorNamespace:  operatorNamespace,
	}

	c.kubeVirtInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addKubeVirt,
		DeleteFunc: c.deleteKubeVirt,
		UpdateFunc: c.updateKubeVirt,
	})

	c.informers.ServiceAccount.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.ServiceAccount)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.ServiceAccount)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.ServiceAccount)
		},
	})

	c.informers.ClusterRole.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.ClusterRole)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.ClusterRole)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.ClusterRole)
		},
	})

	c.informers.ClusterRoleBinding.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.ClusterRoleBinding)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.ClusterRoleBinding)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.ClusterRoleBinding)
		},
	})

	c.informers.Role.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.Role)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.Role)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.Role)
		},
	})

	c.informers.RoleBinding.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.RoleBinding)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.RoleBinding)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.RoleBinding)
		},
	})

	c.informers.Crd.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.Crd)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.Crd)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.Crd)
		},
	})

	c.informers.Service.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.Service)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.Service)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.Service)
		},
	})

	c.informers.Deployment.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.Deployment)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.Deployment)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.Deployment)
		},
	})

	c.informers.DaemonSet.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.DaemonSet)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.DaemonSet)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.DaemonSet)
		},
	})

	c.informers.ValidationWebhook.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.ValidationWebhook)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.ValidationWebhook)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.ValidationWebhook)
		},
	})

	c.informers.InstallStrategyConfigMap.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.InstallStrategyConfigMap)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.InstallStrategyConfigMap)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.InstallStrategyConfigMap)
		},
	})

	c.informers.InstallStrategyJob.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.InstallStrategyJob)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.InstallStrategyJob)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.InstallStrategyJob)
		},
	})

	c.informers.InfrastructurePod.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, nil)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, nil)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, nil)
		},
	})

	c.informers.PodDisruptionBudget.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.genericAddHandler(obj, c.kubeVirtExpectations.PodDisruptionBudget)
		},
		DeleteFunc: func(obj interface{}) {
			c.genericDeleteHandler(obj, c.kubeVirtExpectations.PodDisruptionBudget)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			c.genericUpdateHandler(oldObj, newObj, c.kubeVirtExpectations.PodDisruptionBudget)
		},
	})

	return &c
}

func (c *KubeVirtController) getKubeVirtKey() (string, error) {
	// XXX use owner references instead in general
	kvs := c.kubeVirtInformer.GetStore().List()
	if len(kvs) > 1 {
		log.Log.Errorf("More than one KubeVirt custom resource detected: %v", len(kvs))
		return "", fmt.Errorf("more than one KubeVirt custom resource detected: %v", len(kvs))
	}

	if len(kvs) == 1 {
		kv := kvs[0].(*v1.KubeVirt)
		return controller.KeyFunc(kv)
	}
	return "", nil
}

func (c *KubeVirtController) genericAddHandler(obj interface{}, expecter *controller.UIDTrackingControllerExpectations) {
	o := obj.(metav1.Object)

	if o.GetDeletionTimestamp() != nil {
		// on a restart of the controller manager, it's possible a new o shows up in a state that
		// is already pending deletion. Prevent the o from being a creation observation.
		c.genericDeleteHandler(obj, expecter)
		return
	}

	controllerKey, err := c.getKubeVirtKey()
	if controllerKey != "" && err == nil {
		if expecter != nil {
			expecter.CreationObserved(controllerKey)
		}
		c.queue.Add(controllerKey)
	}
}

// When an object is updated, inform the kubevirt CR about the change
func (c *KubeVirtController) genericUpdateHandler(old, cur interface{}, expecter *controller.UIDTrackingControllerExpectations) {
	curObj := cur.(metav1.Object)
	oldObj := old.(metav1.Object)
	if curObj.GetResourceVersion() == oldObj.GetResourceVersion() {
		// Periodic resync will send update events for all known objects.
		// Two different versions of the same object will always have different RVs.
		return
	}

	if oldObj.GetDeletionTimestamp() == nil && curObj.GetDeletionTimestamp() != nil {
		// having an object marked for deletion is enough to count as a deletion expectation
		c.genericDeleteHandler(curObj, expecter)
		return
	}

	key, err := c.getKubeVirtKey()
	if key != "" && err == nil {
		c.queue.Add(key)
	}
	return
}

// When an object is deleted, mark objects as deleted and wake up the kubevirt CR
func (c *KubeVirtController) genericDeleteHandler(obj interface{}, expecter *controller.UIDTrackingControllerExpectations) {
	var o metav1.Object
	tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
	if ok {
		o, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			log.Log.Reason(fmt.Errorf("tombstone contained object that is not a k8s object %#v", obj)).Error("Failed to process delete notification")
			return
		}
	} else if o, ok = obj.(metav1.Object); !ok {
		log.Log.Reason(fmt.Errorf("couldn't get object from %+v", obj)).Error("Failed to process delete notification")
		return
	}

	k, err := controller.KeyFunc(o)
	if err != nil {
		log.Log.Reason(err).Errorf("could not extract key from k8s object")
		return
	}

	key, err := c.getKubeVirtKey()
	if key != "" && err == nil {
		if expecter != nil {
			expecter.DeletionObserved(key, k)
		}
		c.queue.Add(key)
	}
}

func (c *KubeVirtController) addKubeVirt(obj interface{}) {
	c.enqueueKubeVirt(obj)
}

func (c *KubeVirtController) deleteKubeVirt(obj interface{}) {
	c.enqueueKubeVirt(obj)
}

func (c *KubeVirtController) updateKubeVirt(old, curr interface{}) {
	c.enqueueKubeVirt(curr)
}

func (c *KubeVirtController) enqueueKubeVirt(obj interface{}) {
	logger := log.Log
	kv := obj.(*v1.KubeVirt)
	key, err := controller.KeyFunc(kv)
	if err != nil {
		logger.Object(kv).Reason(err).Error("Failed to extract key from KubeVirt.")
	}
	c.queue.Add(key)
}

func (c *KubeVirtController) Run(threadiness int, stopCh <-chan struct{}) {
	defer controller.HandlePanic()
	defer c.queue.ShutDown()
	log.Log.Info("Starting KubeVirt controller.")

	// Wait for cache sync before we start the controller
	cache.WaitForCacheSync(stopCh, c.kubeVirtInformer.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.ServiceAccount.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.ClusterRole.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.ClusterRoleBinding.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.Role.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.RoleBinding.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.Crd.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.Service.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.Deployment.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.DaemonSet.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.ValidationWebhook.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.SCC.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.InstallStrategyConfigMap.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.InstallStrategyJob.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.InfrastructurePod.HasSynced)
	cache.WaitForCacheSync(stopCh, c.informers.PodDisruptionBudget.HasSynced)

	// Start the actual work
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	log.Log.Info("Stopping KubeVirt controller.")
}

func (c *KubeVirtController) runWorker() {
	for c.Execute() {
	}
}

func (c *KubeVirtController) Execute() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	err := c.execute(key.(string))

	if err != nil {
		log.Log.Reason(err).Errorf("reenqueuing KubeVirt %v", key)
		c.queue.AddRateLimited(key)
	} else {
		log.Log.V(4).Infof("processed KubeVirt %v", key)
		c.queue.Forget(key)
	}
	return true
}

func (c *KubeVirtController) execute(key string) error {

	// Fetch the latest KubeVirt from cache
	obj, exists, err := c.kubeVirtInformer.GetStore().GetByKey(key)

	if err != nil {
		return err
	}

	if !exists {
		// when the resource is gone, deletion was handled already
		log.Log.Infof("KubeVirt resource not found")
		c.kubeVirtExpectations.DeleteExpectations(key)
		return nil
	}

	kv := obj.(*v1.KubeVirt)
	logger := log.Log.Object(kv)

	// this must be first step in execution. Writing the object
	// when api version changes ensures our api stored version is updated.
	if !controller.ObservedLatestApiVersionAnnotation(kv) {
		kv := kv.DeepCopy()
		controller.SetLatestApiVersionAnnotation(kv)
		_, err = c.clientset.KubeVirt(kv.ObjectMeta.Namespace).Update(kv)
		return err
	}

	// If we can't extract the key we can't do anything
	_, err = controller.KeyFunc(kv)
	if err != nil {
		log.Log.Reason(err).Errorf("Could not extract the key from the custom resource, will do nothing and not requeue.")
		return nil
	}

	logger.Info("Handling KubeVirt resource")

	// only process the kubevirt deployment if all expectations are satisfied.
	needsSync := c.kubeVirtExpectations.SatisfiedExpectations(key)
	if !needsSync {
		logger.Info("Waiting for expectations to be fulfilled")
		return nil
	}

	// Adds of all types are not done in one go. We need to set an expectation of 0 so that we can add something
	c.kubeVirtExpectations.ResetExpectations(key)

	var syncError error
	kvCopy := kv.DeepCopy()

	if kv.DeletionTimestamp != nil {
		syncError = c.syncDeletion(kvCopy)
	} else {
		syncError = c.syncDeployment(kvCopy)
	}

	// If we detect a change on KubeVirt we update it
	if !reflect.DeepEqual(kv.Status, kvCopy.Status) ||
		!reflect.DeepEqual(kv.Finalizers, kvCopy.Finalizers) {

		_, err := c.clientset.KubeVirt(kv.Namespace).Update(kvCopy)

		if err != nil {
			logger.Reason(err).Errorf("Could not update the KubeVirt resource.")
			return err
		}
	}

	return syncError
}

func (c *KubeVirtController) generateInstallStrategyJob(config *operatorutil.KubeVirtDeploymentConfig) (*batchv1.Job, error) {

	operatorImage := fmt.Sprintf("%s/%s%s", config.GetImageRegistry(), "virt-operator", components.AddVersionSeparatorPrefix(config.GetOperatorVersion()))
	deploymentConfigJson, err := config.GetJson()
	if err != nil {
		return nil, err
	}

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},

		ObjectMeta: metav1.ObjectMeta{
			Namespace:    c.operatorNamespace,
			GenerateName: fmt.Sprintf("kubevirt-%s-job", config.GetDeploymentID()),
			Labels: map[string]string{
				v1.AppLabel:             "",
				v1.ManagedByLabel:       v1.ManagedByLabelOperatorValue,
				v1.InstallStrategyLabel: "",
			},
			Annotations: map[string]string{
				// Deprecated, keep it for backwards compatibility
				v1.InstallStrategyVersionAnnotation: config.GetKubeVirtVersion(),
				// Deprecated, keep it for backwards compatibility
				v1.InstallStrategyRegistryAnnotation:   config.GetImageRegistry(),
				v1.InstallStrategyIdentifierAnnotation: config.GetDeploymentID(),
			},
		},
		Spec: batchv1.JobSpec{
			Template: k8sv1.PodTemplateSpec{

				Spec: k8sv1.PodSpec{
					ServiceAccountName: "kubevirt-operator",
					RestartPolicy:      k8sv1.RestartPolicyNever,
					Containers: []k8sv1.Container{
						{
							Name:            "install-strategy-upload",
							Image:           operatorImage,
							ImagePullPolicy: config.GetImagePullPolicy(),
							Command: []string{
								"virt-operator",
								"--dump-install-strategy",
							},
							Env: []k8sv1.EnvVar{
								{
									// Deprecated, keep it for backwards compatibility
									Name:  util.OperatorImageEnvName,
									Value: operatorImage,
								},
								{
									// Deprecated, keep it for backwards compatibility
									Name:  util.TargetInstallNamespace,
									Value: config.GetNamespace(),
								},
								{
									// Deprecated, keep it for backwards compatibility
									Name:  util.TargetImagePullPolicy,
									Value: string(config.GetImagePullPolicy()),
								},
								{
									Name:  util.TargetDeploymentConfig,
									Value: deploymentConfigJson,
								},
							},
						},
					},
				},
			},
		},
	}

	return job, nil
}

func (c *KubeVirtController) garbageCollectInstallStrategyJobs() error {
	batch := c.clientset.BatchV1()
	jobs := c.stores.InstallStrategyJobCache.List()

	for _, obj := range jobs {
		job, ok := obj.(*batchv1.Job)
		if !ok {
			continue
		}
		if job.Status.CompletionTime == nil {
			continue
		}

		propagationPolicy := metav1.DeletePropagationForeground
		err := batch.Jobs(job.Namespace).Delete(job.Name, &metav1.DeleteOptions{
			PropagationPolicy: &propagationPolicy,
		})
		if err != nil {
			return err
		}
		log.Log.Object(job).Infof("Garbage collected completed install strategy job")
	}

	return nil
}

func (c *KubeVirtController) getInstallStrategyFromMap(config *operatorutil.KubeVirtDeploymentConfig) (*installstrategy.InstallStrategy, bool) {
	c.installStrategyMutex.Lock()
	defer c.installStrategyMutex.Unlock()

	strategy, ok := c.installStrategyMap[config.GetDeploymentID()]
	return strategy, ok
}

func (c *KubeVirtController) cacheInstallStrategyInMap(strategy *installstrategy.InstallStrategy, config *operatorutil.KubeVirtDeploymentConfig) {

	c.installStrategyMutex.Lock()
	defer c.installStrategyMutex.Unlock()
	c.installStrategyMap[config.GetDeploymentID()] = strategy

}

func (c *KubeVirtController) deleteAllInstallStrategy() error {

	for _, obj := range c.stores.InstallStrategyConfigMapCache.List() {
		configMap, ok := obj.(*k8sv1.ConfigMap)
		if ok && configMap.DeletionTimestamp == nil {
			err := c.clientset.CoreV1().ConfigMaps(configMap.Namespace).Delete(configMap.Name, &metav1.DeleteOptions{})
			if err != nil {
				log.Log.Errorf("Failed to delete configmap %+v: %v", configMap, err)
				return err
			}
		}
	}

	c.installStrategyMutex.Lock()
	defer c.installStrategyMutex.Unlock()
	// reset the local map
	c.installStrategyMap = make(map[string]*installstrategy.InstallStrategy)

	return nil
}

func (c *KubeVirtController) getInstallStrategyJob(config *operatorutil.KubeVirtDeploymentConfig) (*batchv1.Job, bool) {
	objs := c.stores.InstallStrategyJobCache.List()
	for _, obj := range objs {
		if job, ok := obj.(*batchv1.Job); ok {
			if job.Annotations == nil {
				continue
			}

			if idAnno, ok := job.Annotations[v1.InstallStrategyIdentifierAnnotation]; ok && idAnno == config.GetDeploymentID() {
				return job, true
			}

		}
	}
	return nil, false
}

// Loads install strategies into memory, and generates jobs to
// create install strategies that don't exist yet.
func (c *KubeVirtController) loadInstallStrategy(kv *v1.KubeVirt, loadObservedVersion bool) (*installstrategy.InstallStrategy, bool, error) {

	kvkey, err := controller.KeyFunc(kv)
	if err != nil {
		return nil, true, err
	}

	config := operatorutil.GetTargetConfigFromKV(kv)
	if loadObservedVersion {
		config = operatorutil.GetObservedConfigFromKV(kv)
	}

	// 1. see if we already loaded the install strategy
	strategy, ok := c.getInstallStrategyFromMap(config)
	if ok {
		// we already loaded this strategy into memory
		return strategy, false, nil
	}

	// 2. look for install strategy config map in cache.
	strategy, err = installstrategy.LoadInstallStrategyFromCache(c.stores, config)
	if err == nil {
		c.cacheInstallStrategyInMap(strategy, config)
		log.Log.Infof("Loaded install strategy for kubevirt version %s into cache", config.GetKubeVirtVersion())
		return strategy, false, nil
	}

	log.Log.Infof("Install strategy config map not loaded. reason: %v", err)

	// 3. See if we have a pending job in flight for this install strategy.
	batch := c.clientset.BatchV1()
	cachedJob, exists := c.getInstallStrategyJob(config)
	if exists {
		if cachedJob.Status.CompletionTime != nil {
			// job completed but we don't have a install strategy still
			// delete the job and we'll re-execute it once it is removed.

			log.Log.Object(cachedJob).Errorf("Job failed to create install strategy for version %s for namespace %s", config.GetKubeVirtVersion(), config.GetNamespace())
			if cachedJob.DeletionTimestamp == nil {

				// Just in case there's an issue causing the job to fail
				// immediately after being posted, lets perform a rudimentary
				// for of rate-limiting for how quickly we'll re-attempt.
				// TODO there's an alpha feature that lets us set a TTL on the job
				// itself which will ensure it is automatically cleaned up for us
				// after completion. That feature is feature-gated and isn't something
				// we can depend on right now though.
				now := time.Now().UTC().Unix()
				secondsSinceCompletion := now - cachedJob.Status.CompletionTime.UTC().Unix()
				if secondsSinceCompletion < 10 {
					secondsLeft := int64(10)
					if secondsSinceCompletion > 0 {
						secondsLeft = secondsSinceCompletion
					}
					c.queue.AddAfter(kvkey, time.Duration(secondsLeft)*time.Second)

				} else {
					key, err := controller.KeyFunc(cachedJob)
					if err != nil {
						return nil, true, err
					}

					c.kubeVirtExpectations.InstallStrategyJob.AddExpectedDeletion(kvkey, key)
					propagationPolicy := metav1.DeletePropagationForeground
					err = batch.Jobs(cachedJob.Namespace).Delete(cachedJob.Name, &metav1.DeleteOptions{
						PropagationPolicy: &propagationPolicy,
					})
					if err != nil {
						c.kubeVirtExpectations.InstallStrategyJob.DeletionObserved(kvkey, key)

						log.Log.Object(cachedJob).Errorf("Failed to delete job. %v", err)
						return nil, true, err
					}
					log.Log.Object(cachedJob).Errorf("Deleting job for install strategy version %s because configmap was not generated", config.GetKubeVirtVersion())
				}
			}
		}

		// we're either waiting on the job to be deleted or complete.
		log.Log.Object(cachedJob).Errorf("Waiting on install strategy to be posted from job %s", cachedJob.Name)
		return nil, true, nil
	}

	// 4. execute a job to generate the install strategy for the target version of KubeVirt that's being installed/updated
	job, err := c.generateInstallStrategyJob(config)
	if err != nil {
		return nil, true, err
	}
	c.kubeVirtExpectations.InstallStrategyJob.RaiseExpectations(kvkey, 1, 0)
	_, err = batch.Jobs(c.operatorNamespace).Create(job)
	if err != nil {
		c.kubeVirtExpectations.InstallStrategyJob.LowerExpectations(kvkey, 1, 0)
		return nil, true, err
	}
	log.Log.Infof("Created job to generate install strategy configmap for version %s", config.GetKubeVirtVersion())

	// pending is true here because we're waiting on the job
	// to generate the install strategy
	return nil, true, nil
}

func (c *KubeVirtController) checkForActiveInstall(kv *v1.KubeVirt) bool {
	kvs := c.kubeVirtInformer.GetStore().List()
	for _, obj := range kvs {
		if fromStore, ok := obj.(*v1.KubeVirt); ok {
			if fromStore.UID == kv.UID {
				continue
			}
			if isKubeVirtActive(fromStore) {
				return true
			}
		}
	}
	return false

}

func isUpdating(kv *v1.KubeVirt) bool {

	// first check to see if any version has been observed yet.
	// If no version is observed, this means no version has been
	// installed yet, so we can't be updating.
	if kv.Status.ObservedDeploymentID == "" {
		return false
	}

	// At this point we know an observed version exists.
	// if observed doesn't match target in anyway then we are updating.
	if kv.Status.ObservedDeploymentID != kv.Status.TargetDeploymentID {
		return true
	}

	return false
}

func (c *KubeVirtController) syncDeployment(kv *v1.KubeVirt) error {
	var prevStrategy *installstrategy.InstallStrategy
	var targetStrategy *installstrategy.InstallStrategy
	var prevPending bool
	var targetPending bool
	var err error

	logger := log.Log.Object(kv)
	logger.Infof("Handling deployment")

	// check if there is already an active KubeVirt deployment
	// TODO move this into a new validating webhook
	if c.checkForActiveInstall(kv) {
		logger.Warningf("There is already a KubeVirt deployment!")
		util.UpdateCondition(kv, v1.KubeVirtConditionSynchronized, k8sv1.ConditionFalse, ConditionReasonDeploymentFailedExisting, "There is an active KubeVirt deployment")
		return nil
	}

	config := operatorutil.GetTargetConfigFromKV(kv)

	// Record current operator version to status section
	util.SetOperatorVersion(kv)

	// Record the version we're targeting to install
	config.SetTargetDeploymentConfig(kv)

	if kv.Status.Phase == "" {
		kv.Status.Phase = v1.KubeVirtPhaseDeploying
	}

	if isUpdating(kv) {
		util.RemoveCondition(kv, v1.KubeVirtConditionReady)
		util.RemoveCondition(kv, v1.KubeVirtConditionCreated)
		util.UpdateCondition(kv,
			v1.KubeVirtConditionUpdating,
			k8sv1.ConditionTrue,
			ConditionReasonUpdating,
			fmt.Sprintf("Transitioning from previous version %s with registry %s to target version %s using registry %s",
				kv.Status.ObservedKubeVirtVersion,
				kv.Status.ObservedKubeVirtRegistry,
				kv.Status.TargetKubeVirtVersion,
				kv.Status.TargetKubeVirtRegistry))

		// If this is an update, we need to retrieve the install strategy of the
		// previous version. This is only necessary because there are settings
		// related to SCC privileges that we can't infere without the previous
		// strategy.
		prevStrategy, prevPending, err = c.loadInstallStrategy(kv, true)
		if err != nil {
			return err
		}
	}

	targetStrategy, targetPending, err = c.loadInstallStrategy(kv, false)
	if err != nil {
		return err
	}

	// we're waiting on a job to finish and the config map to be created
	if prevPending || targetPending {
		return nil
	}

	// add finalizer to prevent deletion of CR before KubeVirt was undeployed
	util.AddFinalizer(kv)

	// once all the install strategies are loaded, garbage collect any
	// install strategy jobs that were created.
	err = c.garbageCollectInstallStrategyJobs()
	if err != nil {
		return err
	}

	// deploy
	synced, err := installstrategy.SyncAll(kv, prevStrategy, targetStrategy, c.stores, c.clientset, &c.kubeVirtExpectations)

	if err != nil {
		// deployment failed
		util.UpdateCondition(kv, v1.KubeVirtConditionSynchronized, k8sv1.ConditionFalse, ConditionReasonDeploymentFailedError, fmt.Sprintf("An error occurred during deployment: %v", err))

		logger.Errorf("Failed to create all resources: %v", err)
		return err
	}
	util.RemoveCondition(kv, v1.KubeVirtConditionSynchronized)

	// the entire sync can't always occur within a single control loop execution.
	// when synced==true that means SyncAll() has completed and has nothing left to wait on.
	if synced {
		// record the version that has been completely installed
		config.SetObservedDeploymentConfig(kv)

		// add Created condition
		util.UpdateCondition(kv, v1.KubeVirtConditionCreated, k8sv1.ConditionTrue, ConditionReasonDeploymentCreated, "All resources were created.")
		logger.Info("All KubeVirt resources created")

		// check if components are ready
		if c.isReady(kv) {
			logger.Info("All KubeVirt components ready")
			kv.Status.Phase = v1.KubeVirtPhaseDeployed
			util.UpdateCondition(kv, v1.KubeVirtConditionReady, k8sv1.ConditionTrue, ConditionReasonDeploymentReady, "All components are ready.")

			// Remove updating condition
			util.RemoveCondition(kv, v1.KubeVirtConditionUpdating)

			return nil
		}
		util.RemoveCondition(kv, v1.KubeVirtConditionReady)

	} else {
		util.RemoveCondition(kv, v1.KubeVirtConditionCreated)
		util.RemoveCondition(kv, v1.KubeVirtConditionReady)
	}

	logger.Info("Processed deployment for this round")
	return nil
}

func (c *KubeVirtController) isReady(kv *v1.KubeVirt) bool {

	for _, obj := range c.stores.DeploymentCache.List() {
		if deployment, ok := obj.(*appsv1.Deployment); ok {
			if !util.DeploymentIsReady(kv, deployment, c.stores) {
				return false
			}
		}
	}

	for _, obj := range c.stores.DaemonSetCache.List() {
		if daemonset, ok := obj.(*appsv1.DaemonSet); ok {
			if !util.DaemonsetIsReady(kv, daemonset, c.stores) {
				return false
			}
		}
	}

	return true
}

func (c *KubeVirtController) syncDeletion(kv *v1.KubeVirt) error {
	logger := log.Log.Object(kv)
	logger.Info("Handling deletion")

	// set phase to deleting
	kv.Status.Phase = v1.KubeVirtPhaseDeleting

	// remove created and ready conditions
	util.RemoveCondition(kv, v1.KubeVirtConditionCreated)
	util.RemoveCondition(kv, v1.KubeVirtConditionReady)

	// If we still have cached objects around, more deletions need to take place.
	if !c.stores.AllEmpty() {
		strategy, pending, err := c.loadInstallStrategy(kv, false)
		if err != nil {
			return err
		}

		// we're waiting on the job to finish and the config map to be created
		if pending {
			return nil
		}

		err = installstrategy.DeleteAll(kv, strategy, c.stores, c.clientset, &c.kubeVirtExpectations)
		if err != nil {
			// deletion failed
			util.UpdateCondition(kv, v1.KubeVirtConditionSynchronized, k8sv1.ConditionFalse, ConditionReasonDeletionFailedError, fmt.Sprintf("An error occurred during deletion: %v", err))
			return err
		}
	}

	// clear any synchronized error conditions.
	util.RemoveCondition(kv, v1.KubeVirtConditionSynchronized)

	// Once all deletions are complete,
	// garbage collect all install strategies and
	//remove the finalizer so kv object will disappear.
	if c.stores.AllEmpty() {

		err := c.deleteAllInstallStrategy()
		if err != nil {
			// garbage collection of install strategies failed
			util.UpdateCondition(kv, v1.KubeVirtConditionSynchronized, k8sv1.ConditionFalse, ConditionReasonDeletionFailedError, fmt.Sprintf("An error occurred during deletion: %v", err))
			return err
		}

		err = c.garbageCollectInstallStrategyJobs()
		if err != nil {
			return err
		}

		// deletion successful
		kv.Status.Phase = v1.KubeVirtPhaseDeleted

		// remove finalizer
		kv.Finalizers = nil

		logger.Info("KubeVirt deleted")

		return nil
	}

	logger.Info("Processed deletion for this round")
	return nil
}

func isKubeVirtActive(kv *v1.KubeVirt) bool {
	return kv.Status.Phase != v1.KubeVirtPhaseDeleted
}
