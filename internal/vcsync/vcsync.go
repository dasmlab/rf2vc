// Package vcsync keeps vCenter definitions in ConfigMaps (gateway namespace):
//
//   - At startup every ConfigMap labelled rf2vc.dasmlab.org/vcenter=true is
//     merged into the store (ConfigMap fields win; the password comes from the
//     referenced Secret when it exists, otherwise a password typed in the UI is kept).
//   - vCenters created in the UI, and store-only vCenters found at startup, are
//     written to a ConfigMap labelled rf2vc.dasmlab.org/origin=runtime (no password).
//   - Any other value (set rf2vc.dasmlab.org/origin=gitops in Git) belongs to GitOps;
//     rf2vc never writes or deletes those ConfigMaps.
package vcsync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/dasmlab/rf2vc/internal/activity"
	"github.com/dasmlab/rf2vc/internal/kube"
	"github.com/dasmlab/rf2vc/internal/store"
)

const (
	LabelVCenter   = "rf2vc.dasmlab.org/vcenter"
	LabelOrigin    = "rf2vc.dasmlab.org/origin"
	DataKey        = "vcenter.yaml"
	namePrefix     = "rf2vc-vc-"
	defaultPassKey = "password"
)

// Spec is the vcenter.yaml document in a ConfigMap.
type Spec struct {
	ID                string           `yaml:"id,omitempty"`
	Name              string           `yaml:"name"`
	URL               string           `yaml:"url"`
	Username          string           `yaml:"username,omitempty"`
	Insecure          bool             `yaml:"insecure,omitempty"`
	Datacenter        string           `yaml:"datacenter"`
	Datastore         string           `yaml:"datastore,omitempty"`
	Folder            string           `yaml:"folder,omitempty"`
	ISOFolder         string           `yaml:"isoFolder,omitempty"`
	Notes             string           `yaml:"notes,omitempty"`
	DryRun            *bool            `yaml:"dryRun,omitempty"`
	CredentialsSecret *store.SecretRef `yaml:"credentialsSecret,omitempty"`
}

type Sync struct {
	kc *kube.Client
	st *store.Store
}

func New(kc *kube.Client, st *store.Store) *Sync {
	return &Sync{kc: kc, st: st}
}

func (s *Sync) Namespace() string { return s.kc.Namespace }

// Startup loads ConfigMap-defined vCenters, then writes a runtime ConfigMap
// for every vCenter that has none.
func (s *Sync) Startup(ctx context.Context) error {
	cms, err := s.kc.ListConfigMaps(ctx, LabelVCenter+"=true")
	if err != nil {
		return fmt.Errorf("list vCenter ConfigMaps: %w", err)
	}
	fromCM := map[string]bool{}
	for _, cm := range cms {
		vc, err := s.apply(ctx, cm)
		if err != nil {
			log.Printf("vcenter configmap %s: %v", cm.Metadata.Name, err)
			activity.RunErr("configmap", err.Error(), map[string]any{"configMap": cm.Metadata.Name})
			continue
		}
		fromCM[vc.ID] = true
	}
	for _, vc := range s.st.ListVCenters() {
		if fromCM[vc.ID] {
			continue
		}
		if vc.ConfigMap != "" {
			activity.RunWarn("configmap", "ConfigMap is gone; writing it again as runtime", map[string]any{
				"configMap": vc.ConfigMap, "name": vc.Name,
			})
		}
		if err := s.export(ctx, vc); err != nil {
			log.Printf("vcenter %s: write ConfigMap: %v", vc.Name, err)
			activity.RunErr("configmap", err.Error(), map[string]any{"vcenterId": vc.ID, "name": vc.Name})
		}
	}
	return nil
}

func (s *Sync) apply(ctx context.Context, cm kube.ConfigMap) (store.VCenter, error) {
	raw, ok := cm.Data[DataKey]
	if !ok {
		return store.VCenter{}, fmt.Errorf("missing data key %q", DataKey)
	}
	var spec Spec
	if err := yaml.Unmarshal([]byte(raw), &spec); err != nil {
		return store.VCenter{}, fmt.Errorf("parse %s: %w", DataKey, err)
	}
	origin := store.OriginGitOps
	if cm.Metadata.Labels[LabelOrigin] == store.OriginRuntime {
		origin = store.OriginRuntime
	}
	in := store.VCenter{
		ID: spec.ID, Name: spec.Name, URL: spec.URL, Username: spec.Username, Insecure: spec.Insecure,
		Datacenter: spec.Datacenter, Datastore: spec.Datastore, Folder: spec.Folder,
		ISOFolder: spec.ISOFolder, Notes: spec.Notes,
		ConfigMap: cm.Metadata.Name, ConfigOrigin: origin, CredentialsSecret: spec.CredentialsSecret,
	}
	password, secretNote := s.credentials(ctx, spec.CredentialsSecret, &in)
	vc, err := s.st.ApplyConfigVCenter(in, spec.DryRun, password)
	if err != nil {
		return store.VCenter{}, err
	}
	detail := map[string]any{
		"configMap": cm.Metadata.Name, "origin": origin, "name": vc.Name, "vcenterId": vc.ID,
		"password": passwordState(vc),
	}
	if secretNote != "" {
		detail["secret"] = secretNote
	}
	activity.Run("configmap", "vCenter loaded from ConfigMap", detail)
	return vc, nil
}

// credentials reads the referenced Secret; a missing Secret is normal until
// VSO (or someone) creates it, and the UI password is used meanwhile.
func (s *Sync) credentials(ctx context.Context, ref *store.SecretRef, vc *store.VCenter) (string, string) {
	if ref == nil || ref.Name == "" {
		return "", ""
	}
	data, err := s.kc.GetSecretData(ctx, ref.Name)
	if errors.Is(err, kube.ErrNotFound) {
		return "", "Secret " + ref.Name + " not found"
	}
	if err != nil {
		return "", err.Error()
	}
	if ref.UsernameKey != "" {
		if u := strings.TrimSpace(string(data[ref.UsernameKey])); u != "" {
			vc.Username = u
		}
	}
	key := ref.PasswordKey
	if key == "" {
		key = defaultPassKey
	}
	pw := string(data[key])
	if pw == "" {
		return "", fmt.Sprintf("Secret %s has no key %q", ref.Name, key)
	}
	return pw, ""
}

func passwordState(vc store.VCenter) string {
	switch {
	case !vc.HasPassword:
		return "missing"
	case vc.PasswordSource == store.PasswordFromSecret:
		return "from secret"
	default:
		return "manual (UI)"
	}
}

// Saved writes the ConfigMap after a vCenter is created or edited in the UI.
// GitOps-owned vCenters are left alone (edits last until the next restart).
func (s *Sync) Saved(ctx context.Context, id string) error {
	vc, ok := s.st.GetVCenter(id)
	if !ok || vc.ConfigOrigin == store.OriginGitOps {
		return nil
	}
	return s.export(ctx, vc)
}

// CheckDelete refuses to delete a vCenter that GitOps owns; it would come back.
func (s *Sync) CheckDelete(id string) error {
	vc, ok := s.st.GetVCenter(id)
	if ok && vc.ConfigOrigin == store.OriginGitOps {
		return fmt.Errorf("vCenter %q is defined in ConfigMap %s (GitOps); remove it there", vc.Name, vc.ConfigMap)
	}
	return nil
}

// Deleted removes the runtime ConfigMap of a vCenter deleted in the UI.
func (s *Sync) Deleted(ctx context.Context, vc store.VCenter) error {
	if vc.ConfigOrigin != store.OriginRuntime || vc.ConfigMap == "" {
		return nil
	}
	if err := s.kc.DeleteConfigMap(ctx, vc.ConfigMap); err != nil {
		return err
	}
	activity.Run("configmap", "runtime ConfigMap deleted", map[string]any{"configMap": vc.ConfigMap, "name": vc.Name})
	return nil
}

func (s *Sync) export(ctx context.Context, vc store.VCenter) error {
	name := vc.ConfigMap
	var existing *kube.ConfigMap
	if name == "" {
		var err error
		name, existing, err = s.freeName(ctx, vc)
		if err != nil {
			return err
		}
	} else {
		cm, err := s.kc.GetConfigMap(ctx, name)
		switch {
		case err == nil:
			if cm.Metadata.Labels[LabelOrigin] != store.OriginRuntime {
				return fmt.Errorf("ConfigMap %s is not rf2vc-owned (no %s=runtime label); not overwriting", name, LabelOrigin)
			}
			existing = &cm
		case !errors.Is(err, kube.ErrNotFound):
			return err
		}
	}
	ref := vc.CredentialsSecret
	if ref == nil {
		ref = &store.SecretRef{Name: name, PasswordKey: defaultPassKey}
	}
	cm := Render(vc, name, ref)
	verb := "created"
	var err error
	if existing != nil {
		cm.Metadata.ResourceVersion = existing.Metadata.ResourceVersion
		_, err = s.kc.UpdateConfigMap(ctx, cm)
		verb = "updated"
	} else {
		_, err = s.kc.CreateConfigMap(ctx, cm)
	}
	if err != nil {
		return err
	}
	if err := s.st.SetVCenterConfigMap(vc.ID, name, store.OriginRuntime, ref); err != nil {
		return err
	}
	activity.Run("configmap", "runtime ConfigMap "+verb, map[string]any{"configMap": name, "name": vc.Name, "vcenterId": vc.ID})
	return nil
}

// freeName picks rf2vc-vc-<name>, or adds the id when another vCenter holds it.
func (s *Sync) freeName(ctx context.Context, vc store.VCenter) (string, *kube.ConfigMap, error) {
	base := namePrefix + slug(vc.Name, vc.ID)
	for _, name := range []string{base, base + "-" + vc.ID[:min(6, len(vc.ID))]} {
		cm, err := s.kc.GetConfigMap(ctx, name)
		if errors.Is(err, kube.ErrNotFound) {
			return name, nil, nil
		}
		if err != nil {
			return "", nil, err
		}
		var spec Spec
		_ = yaml.Unmarshal([]byte(cm.Data[DataKey]), &spec)
		if spec.ID == vc.ID && cm.Metadata.Labels[LabelOrigin] == store.OriginRuntime {
			return name, &cm, nil
		}
	}
	return "", nil, fmt.Errorf("no free ConfigMap name for vCenter %q", vc.Name)
}

var nonDNS = regexp.MustCompile(`[^a-z0-9-]+`)

func slug(name, fallback string) string {
	s := nonDNS.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(regexp.MustCompile(`-+`).ReplaceAllString(s, "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return fallback
	}
	return s
}

// Render builds the runtime ConfigMap for vc (never includes the password).
func Render(vc store.VCenter, name string, ref *store.SecretRef) kube.ConfigMap {
	spec := Spec{
		ID: vc.ID, Name: vc.Name, URL: vc.URL, Username: vc.Username, Insecure: vc.Insecure,
		Datacenter: vc.Datacenter, Datastore: vc.Datastore, Folder: vc.Folder,
		ISOFolder: vc.ISOFolder, Notes: vc.Notes, CredentialsSecret: ref,
	}
	if vc.DryRun {
		on := true
		spec.DryRun = &on
	}
	b, _ := yaml.Marshal(spec)
	key := ref.PasswordKey
	if key == "" {
		key = defaultPassKey
	}
	return kube.ConfigMap{
		Metadata: kube.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app":        "rf2vc",
				LabelVCenter: "true",
				LabelOrigin:  store.OriginRuntime,
			},
			Annotations: map[string]string{
				"rf2vc.dasmlab.org/note": fmt.Sprintf(
					"Written by rf2vc for a vCenter defined in the UI. The password is not stored here: "+
						"put it in Secret %s (key %s), e.g. via VSO. To manage this vCenter from Git, "+
						"commit this ConfigMap with label %s: gitops.", ref.Name, key, LabelOrigin),
			},
		},
		Data: map[string]string{DataKey: string(b)},
	}
}
