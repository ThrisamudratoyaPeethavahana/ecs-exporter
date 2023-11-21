package collector

import (
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/aws/aws-sdk-go/service/ecs/ecsiface"
	"github.com/aws/aws-sdk-go/service/applicationautoscaling/applicationautoscalingiface"
	"github.com/aws/aws-sdk-go/service/applicationautoscaling"
    
	"github.com/slok/ecs-exporter/log"
	"github.com/slok/ecs-exporter/types"
)

const (
	maxServicesAPI = 50
)

const (
	maxDescribeServicesAPI = 10
)

// ECSGatherer is the interface that implements the methods required to gather ECS data
type ECSGatherer interface {
	GetClusters() ([]*types.ECSCluster, error)
	GetClusterServices(autoScalingClient *ApplicationAutoScalingClient, cluster *types.ECSCluster) ([]*types.ECSService, error)
	GetClusterContainerInstances(cluster *types.ECSCluster) ([]*types.ECSContainerInstance, error)
}

// Generate ECS API mocks running go generate
//go:generate mockgen -source ../vendor/github.com/aws/aws-sdk-go/service/ecs/ecsiface/interface.go -package sdk -destination ../mock/aws/sdk/ecsiface_mock.go

// ECSClient is a wrapper for AWS ecs client that implements helpers to get ECS clusters metrics
type ECSClient struct {
	client        ecsiface.ECSAPI
	apiMaxResults int64
}

// NewECSClient will return an initialized ECSClient
func NewECSClient(s *session.Session) (*ECSClient, error) {
	return &ECSClient{
		client:        ecs.New(s),
		apiMaxResults: 100,
	}, nil
}

// ApplicationAutoScalingClient is a wrapper for AWS AutoscalingClient that gets ECS clusters metrics
type ApplicationAutoScalingClient struct {
	client        applicationautoscalingiface.ApplicationAutoScalingAPI
	apiMaxResults int64
}

//NewApplicationAutoScalingClient will return an initialized ECSClient
func NewApplicationAutoScalingClient(s *session.Session) (*ApplicationAutoScalingClient, error) {
	return &ApplicationAutoScalingClient{
		client:        applicationautoscaling.New(s),
		apiMaxResults: 100,
	}, nil
}
// GetClusters will get the clusters from the ECS API
func (e *ECSClient) GetClusters() ([]*types.ECSCluster, error) {
	cArns := []*string{}
	params := &ecs.ListClustersInput{
		MaxResults: aws.Int64(e.apiMaxResults),
	}

	// Get cluster IDs
	log.Debugf("Getting cluster list for region")
	for {
		resp, err := e.client.ListClusters(params)
		if err != nil {
			return nil, err
		}

		for _, c := range resp.ClusterArns {
			cArns = append(cArns, c)
		}
		if resp.NextToken == nil || aws.StringValue(resp.NextToken) == "" {
			break
		}
		params.NextToken = resp.NextToken
	}

	// Get service descriptions
	// TODO: this has a 100 cluster limit, split calls in 100 by 100
	params2 := &ecs.DescribeClustersInput{
		Clusters: cArns,
	}
	resp2, err := e.client.DescribeClusters(params2)
	if err != nil {
		return nil, err
	}

	cs := []*types.ECSCluster{}
	log.Debugf("Getting cluster descriptions")
	for _, c := range resp2.Clusters {
		ec := &types.ECSCluster{
			ID:   aws.StringValue(c.ClusterArn),
			Name: aws.StringValue(c.ClusterName),
		}
		cs = append(cs, ec)
	}

	log.Debugf("Got %d clusters", len(cs))
	return cs, nil
}

// srvRes Internal  struct used to return error and result from goroutiens
type srvRes struct {
	result []*types.ECSService
	err    error
}

// GetClusterServices will return all the services from a cluster
func (e *ECSClient) GetClusterServices(autoScalingClient *ApplicationAutoScalingClient, cluster *types.ECSCluster) ([]*types.ECSService, error) {

	sArns := []*string{}

	// Get service ids
	params := &ecs.ListServicesInput{
		Cluster:    aws.String(cluster.ID),
		MaxResults: aws.Int64(e.apiMaxResults),
	}

	log.Debugf("Getting service list for cluster: %s", cluster.Name)
	for {
		resp, err := e.client.ListServices(params)
		if err != nil {
			return nil, err
		}

		for _, s := range resp.ServiceArns {
			sArns = append(sArns, s)
		}

		if resp.NextToken == nil || aws.StringValue(resp.NextToken) == "" {
			break
		}
		params.NextToken = resp.NextToken
	}

	res := []*types.ECSService{}
	// If no services then nothing to fetch
	if len(sArns) == 0 {
		log.Debugf("Ignoring services fetching, no services in cluster: %s", cluster.Name)
		return res, nil
	}

	servC := make(chan srvRes)

	// Only can grab 10 services at a time, create calls in blocks of 10 services
	totalGr := 0 // counter for goroutines
	for i := 0; i <= len(sArns)/maxServicesAPI; i++ {
		st := i * maxServicesAPI
		// Check if the last call is neccesary (las call only made when the division remaider is present)
		if st >= len(sArns) {
			break
		}
		end := st + maxServicesAPI
		var spss []*string
		if end > len(sArns) {
			spss = sArns[st:]
		} else {
			spss = sArns[st:end]
		}

		totalGr++
		// Make a call on goroutine for each service blocks
		go func(services []*string) {
			log.Debugf("Getting service descriptions for cluster: %s", cluster.Name)

			var describeServicesResp []*ecs.Service

			for i := 0; i<len(services)/maxDescribeServicesAPI; i++ {
				st := i*maxDescribeServicesAPI
				end = st+maxDescribeServicesAPI
				if end > len(services) {
					end = len(services)
				}
				params := &ecs.DescribeServicesInput{
					Services: services[st:end],
					Cluster:  aws.String(cluster.ID),
				}

				resp, err := e.client.DescribeServices(params)
				if err != nil {
					servC <- srvRes{nil, err}
				}

				//describeServicesResp = append(describeServicesResp[:], resp.Services[:)
				describeServicesResp = append(describeServicesResp, resp.Services...)
			}
            
			ss := []*types.ECSService{}
            
			scalableTargets, err:= getScalableTargets(autoScalingClient, describeServicesResp, cluster.Name)
			if err != nil {
					servC <- srvRes{nil, err}
			}
			for _, s := range describeServicesResp {
				es := &types.ECSService{
					ID:       aws.StringValue(s.ServiceArn),
					Name:     aws.StringValue(s.ServiceName),
					DesiredT: aws.Int64Value(s.DesiredCount),
					RunningT: aws.Int64Value(s.RunningCount),
					PendingT: aws.Int64Value(s.PendingCount),
				}

				if scalableTarget, ok := scalableTargets[es.Name]; ok {
					es.MinT = scalableTarget[0]
					es.MaxT = scalableTarget[1]
				}

				ss = append(ss, es)
			}

			servC <- srvRes{ss, nil}

		}(spss)

	}

	// Get all results
	for i := 0; i < totalGr; i++ {
		gRes := <-servC
		if gRes.err != nil {
			return res, gRes.err
		}
		res = append(res, gRes.result...)
	}

	log.Debugf("Got %d services on cluster %s", len(res), cluster.Name)
	return res, nil
}

// GetClusterContainerInstances will return all the container instances from a cluster
func (e *ECSClient) GetClusterContainerInstances(cluster *types.ECSCluster) ([]*types.ECSContainerInstance, error) {

	// Get list of container instances
	ciArns := []*string{}
	params := &ecs.ListContainerInstancesInput{
		Cluster:    aws.String(cluster.ID),
		MaxResults: aws.Int64(e.apiMaxResults),
	}

	log.Debugf("Getting container instance list for cluster: %s", cluster.Name)
	for {
		resp, err := e.client.ListContainerInstances(params)
		if err != nil {
			return nil, err
		}

		for _, c := range resp.ContainerInstanceArns {
			ciArns = append(ciArns, c)
		}

		if resp.NextToken == nil || aws.StringValue(resp.NextToken) == "" {
			break
		}
		params.NextToken = resp.NextToken
	}

	ciDescs := []*types.ECSContainerInstance{}
	// If no container instances then nothing to fetch
	if len(ciArns) == 0 {
		log.Debugf("Ignoring container instance fetching, no services in cluster: %s", cluster.Name)
		return ciDescs, nil
	}

	// Get description of container instances
	params2 := &ecs.DescribeContainerInstancesInput{
		Cluster:            aws.String(cluster.ID),
		ContainerInstances: ciArns,
	}

	log.Debugf("Getting container instance descriptions for cluster: %s", cluster.Name)
	resp, err := e.client.DescribeContainerInstances(params2)
	if err != nil {
		return nil, err
	}

	for _, c := range resp.ContainerInstances {
		var act bool
		if aws.StringValue(c.Status) == types.ContainerInstanceStatusActive {
			act = true
		}
		cd := &types.ECSContainerInstance{
			ID:         aws.StringValue(c.ContainerInstanceArn),
			InstanceID: aws.StringValue(c.Ec2InstanceId),
			AgentConn:  aws.BoolValue(c.AgentConnected),
			Active:     act,
			PendingT:   aws.Int64Value(c.PendingTasksCount),
		}
		ciDescs = append(ciDescs, cd)
	}

	log.Debugf("Got %d container instance on cluster %s", len(ciDescs), cluster.Name)

	return ciDescs, nil
}
