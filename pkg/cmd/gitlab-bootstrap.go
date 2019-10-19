package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pkg/errors"

	"github.com/spf13/cobra"

	v1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	gitlab "github.com/xanzy/go-gitlab"
)

// Version of the plugin
const Version = "1.0.0"

// GitLabBootstrapOptions holds configs used to make requests
type GitLabBootstrapOptions struct {
	ConfigFlags *genericclioptions.ConfigFlags

	GitLabAPIToken     string
	GitLabProjectID    string
	GitLabURL          string
	GitLabGroupCluster bool

	KubeConfig    string
	RestConfig    *restclient.Config
	KubeAPI       *clientcmdapi.Config
	KubeClientSet *kubernetes.Clientset

	ClusterName string
	ClusterHost string
	ClusterCA   string

	ServiceAccountToken string

	GitLabAPI *gitlab.Client

	genericclioptions.IOStreams
}

// NewGitLabBootstrapOptions provides an instance of GitLabBootstrapOptions with default values
func NewGitLabBootstrapOptions(streams genericclioptions.IOStreams) *GitLabBootstrapOptions {
	return &GitLabBootstrapOptions{
		ConfigFlags: genericclioptions.NewConfigFlags(true),
		IOStreams:   streams,
	}
}

// NewCmdGitLabBootstrap creates and returns a new command
func NewCmdGitLabBootstrap(streams genericclioptions.IOStreams) *cobra.Command {
	o := NewGitLabBootstrapOptions(streams)

	cmd := &cobra.Command{
		Use:     "gitlab-bootstrap [project id]",
		Short:   "Bootstraps a Kubernetes cluster into a GitLab project",
		Version: Version,
		RunE: func(c *cobra.Command, args []string) error {
			if err := o.Complete(c, args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			if err := o.Run(); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&o.GitLabAPIToken, "gitlab-api-token", "", "Private token from GitLab. Pulled from env[\"GITLAB_API_TOKEN\"] if not provided")
	cmd.Flags().StringVar(&o.GitLabURL, "gitlab-url", "", "Set to override default connection to GitLab")
	cmd.Flags().BoolVar(&o.GitLabGroupCluster, "gitlab-use-group", false, "Add the cluster to the group identified by the id rather than a project")
	o.ConfigFlags.AddFlags(cmd.Flags())

	return cmd
}

// Complete sets all configs required
func (o *GitLabBootstrapOptions) Complete(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("GitLab project id is required")
	}
	o.GitLabProjectID = args[0]

	if o.GitLabAPIToken == "" {
		o.GitLabAPIToken = os.Getenv("GITLAB_API_TOKEN")
	}

	// Grab KubeConfig from flag or home dir
	if *o.ConfigFlags.KubeConfig != "" {
		o.KubeConfig = *o.ConfigFlags.KubeConfig
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return errors.Wrap(err, "can't get home dir")
		}

		o.KubeConfig = filepath.Join(home, ".kube", "config")
	}
	config, err := clientcmd.BuildConfigFromFlags("", o.KubeConfig)
	if err != nil {
		return errors.Wrap(err, "error building config from kubeconfig path")
	}
	o.RestConfig = config
	o.ClusterHost = config.Host
	o.ClusterCA = string(config.TLSClientConfig.CAData)

	api, err := clientcmd.LoadFromFile(o.KubeConfig)
	if err != nil {
		return errors.Wrap(err, "error creating clientcmdapi from kubeconfig path")
	}
	o.KubeAPI = api

	if len(api.Contexts) < 1 {
		return fmt.Errorf("no contexts found in kubeconfig")
	}
	if api.CurrentContext == "" {
		return fmt.Errorf("no context currently set")
	}
	o.ClusterName = api.Contexts[api.CurrentContext].Cluster

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return errors.Wrap(err, "error creating clientset from config")
	}
	o.KubeClientSet = clientset

	return nil
}

// Validate ensures that all configs are valid
func (o *GitLabBootstrapOptions) Validate() error {
	if o.GitLabAPIToken == "" {
		return fmt.Errorf("GitLab API token is required")
	}
	if o.GitLabProjectID == "" {
		return fmt.Errorf("GitLab project id is required")
	}
	o.GitLabAPI = gitlab.NewClient(nil, o.GitLabAPIToken)

	if o.GitLabURL != "" {
		o.GitLabAPI.SetBaseURL(o.GitLabURL)
	}

	if (o.GitLabGroupCluster) {
		_, _, err := o.GitLabAPI.Groups.GetGroup(o.GitLabProjectID, nil)
		if err != nil {
			return errors.Wrap(err, "unable to get GitLab group")
		}
	} else {
		_, _, err := o.GitLabAPI.Projects.GetProject(o.GitLabProjectID, nil)
		if err != nil {
			return errors.Wrap(err, "unable to get GitLab project")
		}
	}

	return nil
}

// Run executes the command
func (o *GitLabBootstrapOptions) Run() error {
	if err := o.CreateServiceAccount(); err != nil {
		return err
	}
	if err := o.CreateClusterRoleBinding(); err != nil {
		return err
	}
	if err := o.SaveServiceAccountToken(); err != nil {
		return err
	}
	if err := o.AddClusterToGitLab(); err != nil {
		return err
	}
	return nil
}

// CreateServiceAccount creates the gitlab-admin ServiceAccount
func (o *GitLabBootstrapOptions) CreateServiceAccount() error {
	sai := o.KubeClientSet.CoreV1().ServiceAccounts("kube-system")
	_, err := sai.Get("gitlab-admin", metav1.GetOptions{})
	if err != nil {
		saSpec := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "gitlab-admin"}}
		_, err := sai.Create(saSpec)
		if err != nil {
			return errors.Wrap(err, "unable to create service account")
		}
	} else {
		fmt.Println("Using existing gitlab-admin account")
	}

	return nil
}

// CreateClusterRoleBinding creates the gitlab-admin ClusterRoleBinding
func (o *GitLabBootstrapOptions) CreateClusterRoleBinding() error {
	crbSubject := rbacv1.Subject{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      "gitlab-admin",
		Namespace: "kube-system",
	}
	roleRef := rbacv1.RoleRef{
		Name: "cluster-admin",
		Kind: "ClusterRole",
	}
	crbSpec := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "gitlab-admin"}, Subjects: []rbacv1.Subject{crbSubject}, RoleRef: roleRef}
	_, err := o.KubeClientSet.RbacV1().ClusterRoleBindings().Get("gitlab-admin", metav1.GetOptions{})
	if err != nil {
		_, err := o.KubeClientSet.RbacV1().ClusterRoleBindings().Create(crbSpec)
		if err != nil {
			return errors.Wrap(err, "unable to create clusterrolebinding")
		}
		return nil
	} else {
		fmt.Println("Using existing clusterrolebinding")
	}

	return nil
}

// SaveServiceAccountToken saves the gitlab-admin ServiceAccount token
func (o *GitLabBootstrapOptions) SaveServiceAccountToken() error {
	sai := o.KubeClientSet.CoreV1().ServiceAccounts("kube-system")
	sa, err := sai.Get("gitlab-admin", metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "unable to get serviceaccount")
	}
	var tokenName string
	for _, secret := range sa.Secrets {
		match, err := regexp.MatchString("^gitlab-admin-token-", secret.Name)
		if err != nil {
			return errors.Wrap(err, "error matching regexp")
		}
		if match {
			tokenName = secret.Name
			break
		}
	}

	si := o.KubeClientSet.CoreV1().Secrets("kube-system")
	secret, err := si.Get(tokenName, metav1.GetOptions{})
	if err != nil {
		return errors.Wrap(err, "unable to get serviceaccount token")
	}
	token := string(secret.Data["token"])
	if token == "" {
		return errors.Wrap(err, "no data in serviceaccount token")
	}
	o.ServiceAccountToken = token
	return nil
}

// AddClusterToProject adds the Kubernetes cluster to the GitLab project
func (o *GitLabBootstrapOptions) AddClusterToGitLab() error {

  if (o.GitLabGroupCluster) {
		if err := o.addClusterToGroup(); err != nil {
			return err
		}
	} else {
		if err := o.addClusterToProject(); err != nil {
			return err
		}
	}
	return nil
}

func (o *GitLabBootstrapOptions) addClusterToGroup() error {
	clusterOpts := &gitlab.AddGroupClusterOptions{
		Name:             &o.ClusterName,
		EnvironmentScope: gitlab.String("*"),
		PlatformKubernetes: &gitlab.AddGroupPlatformKubernetesOptions{
			APIURL: &o.ClusterHost,
			Token:  &o.ServiceAccountToken,
			CaCert: &o.ClusterCA,
		},
	}
	gc, _, err := o.GitLabAPI.GroupCluster.AddCluster(o.GitLabProjectID, clusterOpts)
	if err != nil {
		return errors.Wrap(err, "unable to assign kubernetes cluster to group")
	}
	gitlabClusterURL := fmt.Sprintf("%s/clusters/%d", gc.Group.WebURL, gc.ID)
	fmt.Println("Cluster successfully added to group!")
	fmt.Printf("To finish up visit: %s and install Helm and Runner.\n", gitlabClusterURL)
	return nil
}

func (o *GitLabBootstrapOptions) addClusterToProject() error {
	clusterOpts := &gitlab.AddClusterOptions{
		Name:             &o.ClusterName,
		EnvironmentScope: gitlab.String("*"),
		PlatformKubernetes: &gitlab.AddPlatformKubernetesOptions{
			APIURL: &o.ClusterHost,
			Token:  &o.ServiceAccountToken,
			CaCert: &o.ClusterCA,
		},
	}
	pc, _, err := o.GitLabAPI.ProjectCluster.AddCluster(o.GitLabProjectID, clusterOpts)
	if err != nil {
		return errors.Wrap(err, "unable to assign kubernetes cluster to project")
	}
	gitlabClusterURL := fmt.Sprintf("%s/clusters/%d", pc.Project.WebURL, pc.ID)
	fmt.Println("Cluster successfully added to project!")
	fmt.Printf("To finish up visit: %s and install Helm and Runner.\n", gitlabClusterURL)
	return nil
}
