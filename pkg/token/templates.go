package token

import "html/template"

const (
	tokenTemplateText = `apiVersion: v1
kind: Config
clusters:
- name: "{{.ClusterName}}"
  cluster:
    server: "{{.Host}}"

users:
- name: "{{.ClusterName}}"
  user:
    token: "{{.Token}}"

contexts:
- name: "{{.ClusterName}}"
  context:
    user: "{{.ClusterName}}"
    cluster: "{{.ClusterName}}"

current-context: "{{.ClusterName}}"
`
)

var (
	tokenTemplate = template.Must(template.New("tokenTemplate").Parse(tokenTemplateText))
)
