// Copyright 2018 deepfabric, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package member

import (
	"fmt"

	"github.com/deepfabric/elasticell-operator/pkg/apis/deepfabric.com/v1alpha1"
	"github.com/deepfabric/elasticell-operator/pkg/controller"
	"github.com/deepfabric/elasticell-operator/pkg/label"
	"github.com/deepfabric/elasticell-operator/pkg/manager"
	"github.com/deepfabric/elasticell-operator/pkg/util"
	"github.com/golang/glog"
	apps "k8s.io/api/apps/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/listers/apps/v1beta1"
	corelisters "k8s.io/client-go/listers/core/v1"
)

type pdMemberManager struct {
	pdControl  controller.PDControlInterface
	setControl controller.StatefulSetControlInterface
	svcControl controller.ServiceControlInterface
	setLister  v1beta1.StatefulSetLister
	svcLister  corelisters.ServiceLister
	podLister  corelisters.PodLister
	podControl controller.PodControlInterface
	pvcLister  corelisters.PersistentVolumeClaimLister
	pdUpgrader Upgrader
}

// NewPDMemberManager returns a *pdMemberManager
func NewPDMemberManager(pdControl controller.PDControlInterface,
	setControl controller.StatefulSetControlInterface,
	svcControl controller.ServiceControlInterface,
	setLister v1beta1.StatefulSetLister,
	svcLister corelisters.ServiceLister,
	podLister corelisters.PodLister,
	podControl controller.PodControlInterface,
	pvcLister corelisters.PersistentVolumeClaimLister,
	pdUpgrader Upgrader) manager.Manager {
	return &pdMemberManager{
		pdControl,
		setControl,
		svcControl,
		setLister,
		svcLister,
		podLister,
		podControl,
		pvcLister,
		pdUpgrader}
}

func (pdmm *pdMemberManager) Sync(cc *v1alpha1.CellCluster) error {
	// Sync PD Service
	if err := pdmm.syncPDServiceForCellCluster(cc); err != nil {
		return err
	}

	// Sync PD Headless Service
	if err := pdmm.syncPDHeadlessServiceForCellCluster(cc); err != nil {
		return err
	}

	// Sync PD StatefulSet
	return pdmm.syncPDStatefulSetForCellCluster(cc)
}

func (pdmm *pdMemberManager) syncPDServiceForCellCluster(cc *v1alpha1.CellCluster) error {
	ns := cc.GetNamespace()
	ccName := cc.GetName()

	newSvc := pdmm.getNewPDServiceForCellCluster(cc)
	oldSvc, err := pdmm.svcLister.Services(ns).Get(controller.PDMemberName(ccName))
	if errors.IsNotFound(err) {
		err = SetServiceLastAppliedConfigAnnotation(newSvc)
		if err != nil {
			return err
		}
		return pdmm.svcControl.CreateService(cc, newSvc)
	}
	if err != nil {
		return err
	}

	equal, err := serviceEqual(newSvc, oldSvc)
	if err != nil {
		return err
	}
	if !equal {
		svc := *oldSvc
		svc.Spec = newSvc.Spec
		// TODO add unit test
		svc.Spec.ClusterIP = oldSvc.Spec.ClusterIP
		err = SetServiceLastAppliedConfigAnnotation(&svc)
		if err != nil {
			return err
		}
		_, err = pdmm.svcControl.UpdateService(cc, &svc)
		return err
	}

	return nil
}

func (pdmm *pdMemberManager) syncPDHeadlessServiceForCellCluster(cc *v1alpha1.CellCluster) error {
	ns := cc.GetNamespace()
	ccName := cc.GetName()

	newSvc := pdmm.getNewPDHeadlessServiceForCellCluster(cc)
	oldSvc, err := pdmm.svcLister.Services(ns).Get(controller.PDPeerMemberName(ccName))
	if errors.IsNotFound(err) {
		err = SetServiceLastAppliedConfigAnnotation(newSvc)
		if err != nil {
			return err
		}
		return pdmm.svcControl.CreateService(cc, newSvc)
	}
	if err != nil {
		return err
	}

	equal, err := serviceEqual(newSvc, oldSvc)
	if err != nil {
		return err
	}
	if !equal {
		svc := *oldSvc
		svc.Spec = newSvc.Spec
		// TODO add unit test
		svc.Spec.ClusterIP = oldSvc.Spec.ClusterIP
		err = SetServiceLastAppliedConfigAnnotation(newSvc)
		if err != nil {
			return err
		}
		_, err = pdmm.svcControl.UpdateService(cc, &svc)
		return err
	}

	return nil
}

func (pdmm *pdMemberManager) syncPDStatefulSetForCellCluster(cc *v1alpha1.CellCluster) error {
	ns := cc.GetNamespace()
	ccName := cc.GetName()

	newPDSet, err := pdmm.getNewPDSetForCellCluster(cc)
	if err != nil {
		return err
	}

	oldPDSet, err := pdmm.setLister.StatefulSets(ns).Get(controller.PDMemberName(ccName))
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if errors.IsNotFound(err) {
		err = SetLastAppliedConfigAnnotation(newPDSet)
		if err != nil {
			return err
		}
		if err := pdmm.setControl.CreateStatefulSet(cc, newPDSet); err != nil {
			return err
		}
		cc.Status.PD.StatefulSet = &apps.StatefulSetStatus{}
		return controller.RequeueErrorf("CellCluster: [%s/%s], waiting for PD cluster running", ns, ccName)
	}

	if err := pdmm.syncCellClusterStatus(cc, oldPDSet); err != nil {
		glog.Errorf("failed to sync CellCluster: [%s/%s]'s status, error: %v", ns, ccName, err)
	}

	if !templateEqual(newPDSet.Spec.Template, oldPDSet.Spec.Template) || cc.Status.PD.Phase == v1alpha1.UpgradePhase {
		if err := pdmm.pdUpgrader.Upgrade(cc, oldPDSet, newPDSet); err != nil {
			return err
		}
	}

	if *newPDSet.Spec.Replicas != *oldPDSet.Spec.Replicas {
		glog.Errorf("failed to sync CellCluster: [%s/%s]'s status, pd doesn't support scale now! ", ns, ccName)
		return nil
	}

	// TODO FIXME equal is false every time
	if !statefulSetEqual(*newPDSet, *oldPDSet) {
		set := *oldPDSet
		set.Spec.Template = newPDSet.Spec.Template
		*set.Spec.Replicas = *newPDSet.Spec.Replicas
		set.Spec.UpdateStrategy = newPDSet.Spec.UpdateStrategy
		err := SetLastAppliedConfigAnnotation(&set)
		if err != nil {
			return err
		}
		_, err = pdmm.setControl.UpdateStatefulSet(cc, &set)
		return err
	}

	return nil
}

func (pdmm *pdMemberManager) syncCellClusterStatus(cc *v1alpha1.CellCluster, set *apps.StatefulSet) error {
	ns := cc.GetNamespace()
	ccName := cc.GetName()

	cc.Status.PD.StatefulSet = &set.Status

	upgrading, err := pdmm.pdStatefulSetIsUpgrading(set, cc)
	if err != nil {
		return err
	}
	if upgrading {
		cc.Status.PD.Phase = v1alpha1.UpgradePhase
	} else {
		cc.Status.PD.Phase = v1alpha1.NormalPhase
	}

	pdClient := pdmm.pdControl.GetPDClient(cc)

	cluster, err := pdClient.GetCluster()
	if err != nil {
		cc.Status.PD.Synced = false
		return err
	}
	cc.Status.ClusterID = cluster

	healthInfo, err := pdClient.GetHealth()
	if err != nil {
		cc.Status.PD.Synced = false
		return err
	}
	_, err = pdClient.GetPDLeader()
	if err != nil {
		cc.Status.PD.Synced = false
		return err
	}
	pdStatus := map[string]v1alpha1.PDMember{}
	for _, memberHealth := range healthInfo.Healths {
		id := memberHealth.MemberID
		memberID := fmt.Sprintf("%d", id)
		var clientURL string
		if len(memberHealth.ClientUrls) > 0 {
			clientURL = memberHealth.ClientUrls[0]
		}
		name := memberHealth.Name
		if len(name) == 0 {
			glog.Warningf("PD member: [%d] doesn't have a name, and can't get it from clientUrls: [%s], memberHealth Info: [%v] in [%s/%s]",
				id, memberHealth.ClientUrls, memberHealth, ns, ccName)
			continue
		}

		status := v1alpha1.PDMember{
			Name:      name,
			ID:        memberID,
			ClientURL: clientURL,
			Health:    memberHealth.Health,
		}

		oldPDMember, exist := cc.Status.PD.Members[name]
		if exist {
			status.LastTransitionTime = oldPDMember.LastTransitionTime
		}
		if !exist || status.Health != oldPDMember.Health {
			status.LastTransitionTime = metav1.Now()
		}

		pdStatus[name] = status
	}

	cc.Status.PD.Synced = true
	cc.Status.PD.Members = pdStatus
	// cc.Status.PD.Leader = cc.Status.PD.Members[leader.GetName()]
	cc.Status.PD.Leader = v1alpha1.PDMember{}

	return nil
}

func (pdmm *pdMemberManager) getNewPDServiceForCellCluster(cc *v1alpha1.CellCluster) *corev1.Service {
	ns := cc.Namespace
	ccName := cc.Name
	svcName := controller.PDMemberName(ccName)
	instanceName := cc.GetLabels()[label.InstanceLabelKey]
	pdLabel := label.New().Instance(instanceName).PD().Labels()

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            svcName,
			Namespace:       ns,
			Labels:          pdLabel,
			OwnerReferences: []metav1.OwnerReference{controller.GetOwnerRef(cc)},
		},
		Spec: corev1.ServiceSpec{
			Type: controller.GetServiceType(cc.Spec.Services, v1alpha1.PDMemberType.String()),
			Ports: []corev1.ServicePort{
				{
					Name:       "client",
					Port:       2379,
					TargetPort: intstr.FromInt(2379),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: pdLabel,
		},
	}
}

func (pdmm *pdMemberManager) getNewPDHeadlessServiceForCellCluster(cc *v1alpha1.CellCluster) *corev1.Service {
	ns := cc.Namespace
	ccName := cc.Name
	svcName := controller.PDPeerMemberName(ccName)
	instanceName := cc.GetLabels()[label.InstanceLabelKey]
	pdLabel := label.New().Instance(instanceName).PD().Labels()

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            svcName,
			Namespace:       ns,
			Labels:          pdLabel,
			OwnerReferences: []metav1.OwnerReference{controller.GetOwnerRef(cc)},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Ports: []corev1.ServicePort{
				{
					Name:       "peer",
					Port:       2380,
					TargetPort: intstr.FromInt(2380),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "rpc",
					Port:       20800,
					TargetPort: intstr.FromInt(20800),
					Protocol:   corev1.ProtocolTCP,
				},
			},
			Selector: pdLabel,
		},
	}
}

func (pdmm *pdMemberManager) pdStatefulSetIsUpgrading(set *apps.StatefulSet, cc *v1alpha1.CellCluster) (bool, error) {
	if statefulSetIsUpgrading(set) {
		return true, nil
	}
	selector, err := label.New().
		Instance(cc.GetLabels()[label.InstanceLabelKey]).
		PD().
		Selector()
	if err != nil {
		return false, err
	}
	pdPods, err := pdmm.podLister.Pods(cc.GetNamespace()).List(selector)
	if err != nil {
		return false, err
	}
	for _, pod := range pdPods {
		revisionHash, exist := pod.Labels[apps.ControllerRevisionHashLabelKey]
		if !exist {
			return false, nil
		}
		if revisionHash != cc.Status.PD.StatefulSet.UpdateRevision {
			return true, nil
		}
	}
	return false, nil
}

func (pdmm *pdMemberManager) getNewPDSetForCellCluster(cc *v1alpha1.CellCluster) (*apps.StatefulSet, error) {
	ns := cc.Namespace
	ccName := cc.Name
	instanceName := cc.GetLabels()[label.InstanceLabelKey]
	pdConfigMap := controller.PDMemberName(ccName)

	annMount, annVolume := annotationsMountVolume()
	volMounts := []corev1.VolumeMount{
		annMount,
		{Name: "config", ReadOnly: true, MountPath: "/etc/pd"},
		{Name: "startup-script", ReadOnly: true, MountPath: "/usr/local/bin/startup"},
		{Name: v1alpha1.PDMemberType.String(), MountPath: "/var/lib/pd"},
	}
	vols := []corev1.Volume{
		annVolume,
		{Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: pdConfigMap,
					},
					Items: []corev1.KeyToPath{{Key: "config-file", Path: "pd.toml"}},
				},
			},
		},
		{Name: "startup-script",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: pdConfigMap,
					},
					Items: []corev1.KeyToPath{{Key: "startup-script", Path: "pd_start_script.sh"}},
				},
			},
		},
	}

	var q resource.Quantity
	var err error
	if cc.Spec.PD.Requests != nil {
		size := cc.Spec.PD.Requests.Storage
		q, err = resource.ParseQuantity(size)
		if err != nil {
			return nil, fmt.Errorf("cant' get storage size: %s for CellCluster: %s/%s, %v", size, ns, ccName, err)
		}
	}
	pdLabel := label.New().Instance(instanceName).PD()
	setName := controller.PDMemberName(ccName)
	storageClassName := cc.Spec.PD.StorageClassName
	if storageClassName == "" {
		storageClassName = controller.DefaultStorageClassName
	}
	failureReplicas := 0
	for _, failureMember := range cc.Status.PD.FailureMembers {
		if failureMember.MemberDeleted {
			failureReplicas++
		}
	}

	pdSet := &apps.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            setName,
			Namespace:       ns,
			Labels:          pdLabel.Labels(),
			OwnerReferences: []metav1.OwnerReference{controller.GetOwnerRef(cc)},
		},
		Spec: apps.StatefulSetSpec{
			Replicas: func() *int32 { r := cc.Spec.PD.Replicas + int32(failureReplicas); return &r }(),
			Selector: pdLabel.LabelSelector(),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: pdLabel.Labels(),
				},
				Spec: corev1.PodSpec{
					SchedulerName: cc.Spec.SchedulerName,
					Affinity: util.AffinityForNodeSelector(
						ns,
						cc.Spec.PD.NodeSelectorRequired,
						label.New().Instance(instanceName).PD(),
						cc.Spec.PD.NodeSelector,
					),
					Containers: []corev1.Container{
						{
							Name:            v1alpha1.PDMemberType.String(),
							Image:           cc.Spec.PD.Image,
							Command:         []string{"/bin/sh", "/usr/local/bin/startup/pd_start_script.sh"},
							ImagePullPolicy: cc.Spec.PD.ImagePullPolicy,
							Ports: []corev1.ContainerPort{
								{
									Name:          "peer",
									ContainerPort: int32(2380),
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "client",
									ContainerPort: int32(2379),
									Protocol:      corev1.ProtocolTCP,
								},
								{
									Name:          "rpc",
									ContainerPort: int32(20800),
									Protocol:      corev1.ProtocolTCP,
								},
							},
							VolumeMounts: volMounts,
							Resources:    util.ResourceRequirement(cc.Spec.PD.ContainerSpec),
							Env: []corev1.EnvVar{
								{
									Name: "NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
								{
									Name:  "PEER_SERVICE_NAME",
									Value: controller.PDPeerMemberName(ccName),
								},
								/*
									{
										Name:  "SET_NAME",
										Value: setName,
									},
								*/
								{
									Name:  "TZ",
									Value: cc.Spec.Timezone,
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyAlways,
					Tolerations:   cc.Spec.PD.Tolerations,
					Volumes:       vols,
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: v1alpha1.PDMemberType.String(),
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						StorageClassName: &storageClassName,
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: q,
							},
						},
					},
				},
			},
			ServiceName:         controller.PDPeerMemberName(ccName),
			PodManagementPolicy: apps.ParallelPodManagement,
			UpdateStrategy: apps.StatefulSetUpdateStrategy{
				Type: apps.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &apps.RollingUpdateStatefulSetStrategy{
					Partition: func() *int32 { r := cc.Spec.PD.Replicas + int32(failureReplicas); return &r }(),
				}},
		},
	}

	return pdSet, nil
}

type FakePDMemberManager struct {
	err error
}

func NewFakePDMemberManager() *FakePDMemberManager {
	return &FakePDMemberManager{}
}

func (fpmm *FakePDMemberManager) SetSyncError(err error) {
	fpmm.err = err
}

func (fpmm *FakePDMemberManager) Sync(_ *v1alpha1.CellCluster) error {
	if fpmm.err != nil {
		return fpmm.err
	}
	return nil
}
