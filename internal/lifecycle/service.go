package lifecycle

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MinTTL = 30 * time.Second
	MaxTTL = 24 * time.Hour
)

var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type Service struct {
	Client client.Client
	Now    func() time.Time
}

type CreateInput struct {
	Name       string `json:"name" jsonschema:"DNS label name, maximum 59 characters"`
	Tier       string `json:"tier,omitempty" jsonschema:"Operator-owned tier name; defaults to stub"`
	TTLSeconds int64  `json:"ttlSeconds" jsonschema:"Lifetime in seconds from now"`
}

type NameInput struct {
	Name string `json:"name"`
}
type RenewInput struct {
	Name       string `json:"name"`
	TTLSeconds int64  `json:"ttlSeconds"`
}
type ListInput struct{}

type View struct {
	Name              string          `json:"name"`
	Tier              string          `json:"tier"`
	ExpiresAt         string          `json:"expiresAt"`
	Phase             string          `json:"phase"`
	VirtualEndpointID string          `json:"virtualEndpointId,omitempty"`
	Conditions        []ConditionView `json:"conditions,omitempty"`
}

type ConditionView struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	LastTransitionTime string `json:"lastTransitionTime"`
}

type DeleteOutput struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type ListOutput struct {
	Items []View `json:"items"`
}

func (s *Service) Create(ctx context.Context, in CreateInput) (View, error) {
	if err := validateName(in.Name); err != nil {
		return View{}, err
	}
	if err := validateTTL(in.TTLSeconds); err != nil {
		return View{}, err
	}
	if in.Tier == "" {
		in.Tier = oubv1.TierStub
	}
	if in.Tier != oubv1.TierStub {
		return View{}, fmt.Errorf("tier %q is not enabled", in.Tier)
	}
	expires := metav1.NewTime(s.now().Add(time.Duration(in.TTLSeconds) * time.Second))
	obj := &oubv1.Oubliette{
		TypeMeta:   metav1.TypeMeta{APIVersion: oubv1.GroupVersion.String(), Kind: "Oubliette"},
		ObjectMeta: metav1.ObjectMeta{Name: in.Name},
		Spec:       oubv1.OublietteSpec{Tier: in.Tier, ExpiresAt: expires},
	}
	if err := s.Client.Create(ctx, obj); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return View{}, err
		}
		if err := s.Client.Get(ctx, client.ObjectKey{Name: in.Name}, obj); err != nil {
			return View{}, err
		}
		if obj.Spec.Tier != in.Tier {
			return View{}, fmt.Errorf("%s already exists with tier %s", in.Name, obj.Spec.Tier)
		}
	}
	return project(obj), nil
}

func (s *Service) Get(ctx context.Context, in NameInput) (View, error) {
	if err := validateName(in.Name); err != nil {
		return View{}, err
	}
	var obj oubv1.Oubliette
	if err := s.Client.Get(ctx, client.ObjectKey{Name: in.Name}, &obj); err != nil {
		return View{}, err
	}
	return project(&obj), nil
}

func (s *Service) List(ctx context.Context, _ ListInput) (ListOutput, error) {
	var list oubv1.OublietteList
	if err := s.Client.List(ctx, &list); err != nil {
		return ListOutput{}, err
	}
	out := ListOutput{Items: make([]View, 0, len(list.Items))}
	for i := range list.Items {
		out.Items = append(out.Items, project(&list.Items[i]))
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Name < out.Items[j].Name })
	return out, nil
}

func (s *Service) Renew(ctx context.Context, in RenewInput) (View, error) {
	if err := validateName(in.Name); err != nil {
		return View{}, err
	}
	if err := validateTTL(in.TTLSeconds); err != nil {
		return View{}, err
	}
	var obj oubv1.Oubliette
	if err := s.Client.Get(ctx, client.ObjectKey{Name: in.Name}, &obj); err != nil {
		return View{}, err
	}
	if apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionForgotten) {
		return View{}, fmt.Errorf("%s is terminal", in.Name)
	}
	candidate := s.now().Add(time.Duration(in.TTLSeconds) * time.Second)
	if candidate.After(obj.Spec.ExpiresAt.Time) {
		obj.Spec.ExpiresAt = metav1.NewTime(candidate)
		if err := s.Client.Update(ctx, &obj); err != nil {
			return View{}, err
		}
	}
	return project(&obj), nil
}

func (s *Service) Delete(ctx context.Context, in NameInput) (DeleteOutput, error) {
	if err := validateName(in.Name); err != nil {
		return DeleteOutput{}, err
	}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: in.Name}}
	if err := s.Client.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return DeleteOutput{}, err
	}
	return DeleteOutput{Name: in.Name, Status: "deleting"}, nil
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > 59 || !nameRE.MatchString(name) {
		return fmt.Errorf("name must be a DNS label of at most 59 characters")
	}
	return nil
}

func validateTTL(seconds int64) error {
	d := time.Duration(seconds) * time.Second
	if d < MinTTL || d > MaxTTL {
		return fmt.Errorf("ttlSeconds must be between %d and %d", int64(MinTTL.Seconds()), int64(MaxTTL.Seconds()))
	}
	return nil
}

func project(obj *oubv1.Oubliette) View {
	phase := "Provisioning"
	switch {
	case !obj.DeletionTimestamp.IsZero():
		phase = "Deleting"
	case apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionForgotten):
		phase = "Forgotten"
	case apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionExpiring):
		phase = "Expiring"
	case apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionReady):
		phase = "Ready"
	}
	conditions := make([]ConditionView, 0, len(obj.Status.Conditions))
	for _, c := range obj.Status.Conditions {
		conditions = append(conditions, ConditionView{Type: c.Type, Status: string(c.Status), Reason: c.Reason, Message: c.Message, LastTransitionTime: c.LastTransitionTime.UTC().Format(time.RFC3339)})
	}
	return View{Name: obj.Name, Tier: obj.Spec.Tier, ExpiresAt: obj.Spec.ExpiresAt.UTC().Format(time.RFC3339), Phase: phase, VirtualEndpointID: obj.Status.VirtualEndpoint, Conditions: conditions}
}
