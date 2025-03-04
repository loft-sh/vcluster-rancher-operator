package unstructured

import (
	"context"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Client struct {
	client.Client
}

func (c *Client) Get(ctx context.Context, gvk schema.GroupVersionKind, name, namespace string) (unstructured.Unstructured, error) {
	obj := unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	err := c.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &obj)
	if err != nil {
		return unstructured.Unstructured{}, err
	}

	return obj, nil
}

func (c *Client) Create(ctx context.Context, gvk schema.GroupVersionKind, name, namespace string, labels map[string]string, object map[string]interface{}) (unstructured.Unstructured, error) {
	obj := unstructured.Unstructured{Object: object}
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.SetGroupVersionKind(gvk)
	obj.SetLabels(labels)
	err := c.Client.Create(ctx, &obj)
	if err != nil {
		return unstructured.Unstructured{}, err
	}

	err = c.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, &obj)
	if err != nil {
		return unstructured.Unstructured{}, err
	}

	return obj, nil
}

func (c *Client) ListWithLabels(ctx context.Context, gvk schema.GroupVersionKind, key, value string) (unstructured.UnstructuredList, error) {
	list := unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}
	matchReq, err := labels.NewRequirement(key, selection.Equals, []string{value})
	if err != nil {
		return unstructured.UnstructuredList{}, err
	}

	list.SetGroupVersionKind(gvk)
	err = c.Client.List(ctx, &list, &client.ListOptions{LabelSelector: labels.NewSelector().Add(*matchReq)})
	if err != nil {
		return unstructured.UnstructuredList{}, err
	}

	return list, nil
}

func (c *Client) GetFirstWithLabel(ctx context.Context, gvk schema.GroupVersionKind, key, value string) (unstructured.Unstructured, error) {
	list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}

	matchReq, err := labels.NewRequirement(key, selection.Equals, []string{value})
	if err != nil {
		return unstructured.Unstructured{}, err
	}

	list.SetGroupVersionKind(gvk)
	err = c.List(ctx, list, &client.ListOptions{LabelSelector: labels.NewSelector().Add(*matchReq)})
	if err != nil && !errors.IsNotFound(err) {
		return unstructured.Unstructured{}, err
	}

	if len(list.Items) == 0 {
		return unstructured.Unstructured{}, errors.NewNotFound(schema.GroupResource{Group: gvk.Group, Resource: gvk.Kind}, "")
	}

	return list.Items[0], nil
}
