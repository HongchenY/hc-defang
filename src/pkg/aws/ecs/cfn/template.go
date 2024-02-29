package cfn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/aws/smithy-go/ptr"
	"github.com/awslabs/goformation/v7/cloudformation"
	"github.com/awslabs/goformation/v7/cloudformation/ec2"
	"github.com/awslabs/goformation/v7/cloudformation/ecr"
	"github.com/awslabs/goformation/v7/cloudformation/ecs"
	"github.com/awslabs/goformation/v7/cloudformation/iam"
	"github.com/awslabs/goformation/v7/cloudformation/logs"
	"github.com/awslabs/goformation/v7/cloudformation/s3"
	"github.com/awslabs/goformation/v7/cloudformation/secretsmanager"
	"github.com/awslabs/goformation/v7/cloudformation/tags"
	awsecs "github.com/defang-io/defang/src/pkg/aws/ecs"
	"github.com/defang-io/defang/src/pkg/aws/ecs/cfn/outputs"
)

const (
	createVpcResources   = true // TODO: make this configurable, add an option to use the default VPC
	maxCachePrefixLength = 20   // prefix must be 2-20 characters long; should be 30 https://github.com/hashicorp/terraform-provider-aws/pull/34716
)

var (
	dockerHubUsername    = os.Getenv("DOCKERHUB_USERNAME") // TODO: support DOCKER_AUTH_CONFIG
	dockerHubAccessToken = os.Getenv("DOCKERHUB_ACCESS_TOKEN")
)

func getCacheRepoPrefix(prefix, suffix string) string {
	repo := prefix + suffix
	if len(repo) > maxCachePrefixLength {
		// Cache repo name is too long; hash it and use the first 6 chars
		hash := sha256.Sum256([]byte(prefix))
		return hex.EncodeToString(hash[:])[:6] + "-" + suffix
	}
	return repo
}

func createTemplate(stack, image string, memory float64, vcpu float64, spot bool, arch *string) *cloudformation.Template {
	prefix := stack + "-"

	defaultTags := []tags.Tag{
		{
			Key:   "CreatedBy",
			Value: awsecs.ProjectName,
		},
	}

	template := cloudformation.NewTemplate()

	// 1. bucket (for state)
	const _bucket = "Bucket"
	template.Resources[_bucket] = &s3.Bucket{
		Tags: defaultTags,
		// BucketName: ptr.String(PREFIX + "bucket" + SUFFIX), // optional; TODO: might want to fix this name to allow Pulumi destroy after stack deletion
		AWSCloudFormationDeletionPolicy: "RetainExceptOnCreate",
		VersioningConfiguration: &s3.Bucket_VersioningConfiguration{
			Status: "Enabled",
		},
	}

	// 2. ECS cluster
	const _cluster = "Cluster"
	template.Resources[_cluster] = &ecs.Cluster{
		Tags: defaultTags,
		// ClusterName: ptr.String(PREFIX + "cluster" + SUFFIX), // optional
	}

	// 3. ECS capacity provider
	capacityProvider := "FARGATE"
	if spot {
		capacityProvider = "FARGATE_SPOT"
	}
	const _capacityProvider = "CapacityProvider"
	template.Resources[_capacityProvider] = &ecs.ClusterCapacityProviderAssociations{
		Cluster: cloudformation.Ref(_cluster),
		CapacityProviders: []string{
			capacityProvider,
		},
		DefaultCapacityProviderStrategy: []ecs.ClusterCapacityProviderAssociations_CapacityProviderStrategy{
			{
				CapacityProvider: capacityProvider,
				Weight:           ptr.Int(1),
			},
		},
	}

	// 4. CloudWatch log group
	const _logGroup = "LogGroup"
	template.Resources[_logGroup] = &logs.LogGroup{
		Tags: defaultTags,
		// LogGroupName:    ptr.String(PREFIX + "log-group-test" + SUFFIX), // optional
		RetentionInDays: ptr.Int(1),
		// Make sure the log group cannot be deleted while the cluster is up
		AWSCloudFormationDependsOn: []string{
			_cluster,
		},
	}

	const _pullThroughCache = "PullThroughCache"
	const _privateRepoSecret = "PrivateRepoSecret"
	// 5. ECR pull-through cache rules
	// TODO: Creating pull through cache rules isn't supported in the following Regions:
	// * China (Beijing) (cn-north-1)
	// * China (Ningxia) (cn-northwest-1)
	// * AWS GovCloud (US-East) (us-gov-east-1)
	// * AWS GovCloud (US-West) (us-gov-west-1)
	if repo, ok := strings.CutPrefix(image, awsecs.EcrPublicRegistry); ok {
		const _pullThroughCache = "PullThroughCache"
		ecrPublicPrefix := getCacheRepoPrefix(prefix, "ecr-public")

		// 5. The pull-through cache rule
		template.Resources[_pullThroughCache] = &ecr.PullThroughCacheRule{
			EcrRepositoryPrefix: ptr.String(ecrPublicPrefix),
			UpstreamRegistryUrl: ptr.String(awsecs.EcrPublicRegistry),
		}

		image = cloudformation.Sub("${AWS::AccountId}.dkr.ecr.${AWS::Region}.amazonaws.com/" + ecrPublicPrefix + repo)
	} else if repo, ok := strings.CutPrefix(image, awsecs.DockerRegistry); ok && dockerHubUsername != "" && dockerHubAccessToken != "" {
		dockerPublicPrefix := getCacheRepoPrefix(prefix, "docker-public")

		// 5a. When creating the Secrets Manager secret that contains the upstream registry credentials, the secret name must use the `ecr-pullthroughcache/` prefix.
		// This is the struct AWS wants, see https://docs.aws.amazon.com/AmazonECR/latest/userguide/pull-through-cache-creating-secret.html
		bytes, err := json.Marshal(struct {
			Username    string `json:"username"`
			AccessToken string `json:"accessToken"`
		}{dockerHubUsername, dockerHubAccessToken})
		if err != nil {
			panic(err) // there's no reason this should ever fail
		}

		// This is $0.40 per secret per month, so make it opt-in (only done when DOCKERHUB_* env vars are set)
		template.Resources[_privateRepoSecret] = &secretsmanager.Secret{
			Tags:         defaultTags,
			Description:  ptr.String("Docker Hub credentials for the ECR pull-through cache rule"),
			Name:         ptr.String("ecr-pullthroughcache/" + dockerPublicPrefix),
			SecretString: ptr.String(string(bytes)),
		}

		// 5b. The pull-through cache rule
		template.Resources[_pullThroughCache] = &ecr.PullThroughCacheRule{
			EcrRepositoryPrefix: ptr.String(dockerPublicPrefix),
			UpstreamRegistryUrl: ptr.String("registry-1.docker.io"),
			CredentialArn:       cloudformation.RefPtr(_privateRepoSecret),
		}

		image = cloudformation.Sub("${AWS::AccountId}.dkr.ecr.${AWS::Region}.amazonaws.com/" + dockerPublicPrefix + repo)
	} else {
		// TODO: support pull through cache for other registries
		// TODO: support private repos (with or without pull-through cache)
	}

	// 6. IAM roles for ECS task
	assumeRolePolicyDocumentECS := map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Effect": "Allow",
				"Principal": map[string]any{
					"Service": []string{
						"ecs-tasks.amazonaws.com",
					},
				},
				"Action": []string{
					"sts:AssumeRole",
				},
			},
		},
	}

	// 6a. IAM exec role for task
	execPolicies := []iam.Role_Policy{{
		// From https://docs.aws.amazon.com/AmazonECR/latest/userguide/pull-through-cache.html#pull-through-cache-iam
		PolicyName: "AllowECRPassThrough",
		PolicyDocument: map[string]any{
			"Version": "2012-10-17",
			"Statement": []map[string]any{
				{
					"Effect": "Allow",
					"Action": []string{
						"ecr:CreatePullThroughCacheRule",
						"ecr:BatchImportUpstreamImage", // should be registry permission instead
						"ecr:CreateRepository",         // can be registry permission instead
					},
					"Resource": "*", // FIXME: restrict cloudformation.Sub("arn:${AWS::Partition}:ecr:${AWS::Region}:${AWS::AccountId}:repository/${PullThroughCache}:*"),
				},
			},
		},
	}}
	if _, ok := template.Resources[_privateRepoSecret]; ok {
		execPolicies = append(execPolicies, iam.Role_Policy{
			PolicyName: "AllowGetRepoSecret",
			PolicyDocument: map[string]any{
				"Version": "2012-10-17",
				"Statement": []map[string]any{
					{
						"Effect": "Allow",
						"Action": []string{
							"secretsmanager:GetSecretValue",
							"ssm:GetParameters",
							// "kms:Decrypt", Required only if your key uses a custom KMS key and not the default key
						},
						"Resource": cloudformation.Ref(_privateRepoSecret),
					},
				},
			},
		})
	}
	const _executionRole = "ExecutionRole"
	template.Resources[_executionRole] = &iam.Role{
		Tags: defaultTags,
		// RoleName: ptr.String(PREFIX + "execution-role" + SUFFIX), // optional
		ManagedPolicyArns: []string{
			"arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy",
		},
		AssumeRolePolicyDocument: assumeRolePolicyDocumentECS,
		Policies:                 execPolicies,
	}

	// 6b. IAM role for task (optional)
	const _taskRole = "TaskRole"
	template.Resources[_taskRole] = &iam.Role{
		Tags: defaultTags,
		// RoleName: ptr.String(PREFIX + "task-role" + SUFFIX), // optional
		ManagedPolicyArns: []string{
			"arn:aws:iam::aws:policy/AdministratorAccess", // TODO: make this configurable
		},
		AssumeRolePolicyDocument: assumeRolePolicyDocumentECS,
		Policies: []iam.Role_Policy{
			{
				// From https://docs.aws.amazon.com/AmazonECS/latest/developerguide/task-iam-roles.html#ecs-exec-required-iam-permissions
				PolicyName: "AllowExecuteCommand",
				PolicyDocument: map[string]any{
					"Version": "2012-10-17",
					"Statement": []map[string]any{
						{
							"Effect": "Allow",
							"Action": []string{
								"ssmmessages:CreateDataChannel",
								"ssmmessages:OpenDataChannel",
								"ssmmessages:OpenControlChannel",
								"ssmmessages:CreateControlChannel",
							},
							"Resource": "*", // TODO: restrict
						},
					},
				},
			},
			{
				PolicyName: "AllowPassRole",
				PolicyDocument: map[string]any{
					"Version": "2012-10-17",
					"Statement": []map[string]any{
						{
							"Effect": "Allow",
							"Action": []string{
								"iam:PassRole",
							},
							"Resource": "*", // TODO: restrict to roles that are needed/created by the task
						},
					},
				},
			},
			{
				PolicyName: "AllowAssumeRole",
				PolicyDocument: map[string]any{
					"Version": "2012-10-17",
					"Statement": []map[string]any{
						{
							"Effect": "Allow",
							"Action": []string{
								"sts:AssumeRole",
							},
							"Resource": "*",
						},
					},
				},
			},
		},
	}

	// 7. ECS task definition
	cpu, mem := awsecs.FixupFargateConfig(vcpu, memory)
	const _taskDefinition = "TaskDefinition"
	template.Resources[_taskDefinition] = &ecs.TaskDefinition{
		Tags: defaultTags,
		RuntimePlatform: &ecs.TaskDefinition_RuntimePlatform{
			CpuArchitecture:       arch,
			OperatingSystemFamily: ptr.String("LINUX"), // TODO: support other OSes?
		},
		ContainerDefinitions: []ecs.TaskDefinition_ContainerDefinition{
			{
				Name:        awsecs.ContainerName,
				Image:       image,
				StopTimeout: ptr.Int(120),
				// LinuxParameters: &ecs.TaskDefinition_LinuxParameters{
				// 	InitProcessEnabled: ptr.Bool(true),
				// },
				LogConfiguration: &ecs.TaskDefinition_LogConfiguration{
					LogDriver: "awslogs",
					Options: map[string]string{
						"awslogs-group":         cloudformation.Ref(_logGroup),
						"awslogs-region":        cloudformation.Ref("AWS::Region"),
						"awslogs-stream-prefix": awsecs.AwsLogsStreamPrefix,
					},
				},
			},
		},
		Cpu:                     ptr.String(strconv.FormatUint(uint64(cpu), 10)),
		ExecutionRoleArn:        cloudformation.RefPtr(_executionRole),
		Memory:                  ptr.String(strconv.FormatUint(uint64(mem), 10)),
		NetworkMode:             ptr.String("awsvpc"),
		RequiresCompatibilities: []string{"FARGATE"},
		TaskRoleArn:             cloudformation.RefPtr(_taskRole),
		// Family:                  cloudformation.SubPtr("${AWS::StackName}-TaskDefinition"), // optional, but needed to avoid TaskDef replacement
	}

	var vpcId *string
	if createVpcResources {
		// 8a. a VPC
		const _vpc = "VPC"
		template.Resources[_vpc] = &ec2.VPC{
			Tags:      append([]tags.Tag{{Key: "Name", Value: prefix + "vpc"}}, defaultTags...),
			CidrBlock: ptr.String("10.0.0.0/16"),
		}
		vpcId = cloudformation.RefPtr(_vpc)
		// 8b. an internet gateway; FIXME: make internet access optional
		const _internetGateway = "InternetGateway"
		template.Resources[_internetGateway] = &ec2.InternetGateway{
			Tags: append([]tags.Tag{{Key: "Name", Value: prefix + "igw"}}, defaultTags...),
		}
		// 8c. an internet gateway attachment for the VPC
		const _internetGatewayAttachment = "InternetGatewayAttachment"
		template.Resources[_internetGatewayAttachment] = &ec2.VPCGatewayAttachment{
			VpcId:             cloudformation.Ref(_vpc),
			InternetGatewayId: cloudformation.RefPtr(_internetGateway),
		}
		// 8d. a route table
		const _routeTable = "RouteTable"
		template.Resources[_routeTable] = &ec2.RouteTable{
			Tags:  append([]tags.Tag{{Key: "Name", Value: prefix + "routetable"}}, defaultTags...),
			VpcId: cloudformation.Ref(_vpc),
		}
		// 8e. a route for the route table and internet gateway
		const _route = "Route"
		template.Resources[_route] = &ec2.Route{
			RouteTableId:         cloudformation.Ref(_routeTable),
			DestinationCidrBlock: ptr.String("0.0.0.0/0"),
			GatewayId:            cloudformation.RefPtr(_internetGateway),
		}
		// 8f. a public subnet
		const _subnet = "Subnet"
		template.Resources[_subnet] = &ec2.Subnet{
			Tags: append([]tags.Tag{{Key: "Name", Value: prefix + "subnet"}}, defaultTags...),
			// AvailabilityZone:; TODO: parse region suffix
			CidrBlock:           ptr.String("10.0.0.0/20"),
			VpcId:               cloudformation.Ref(_vpc),
			MapPublicIpOnLaunch: ptr.Bool(true),
		}
		// 8g. a subnet / route table association
		const _subnetRouteTableAssociation = "SubnetRouteTableAssociation"
		template.Resources[_subnetRouteTableAssociation] = &ec2.SubnetRouteTableAssociation{
			SubnetId:     cloudformation.Ref(_subnet),
			RouteTableId: cloudformation.Ref(_routeTable),
		}
		// 8h. S3 gateway endpoint (to avoid S3 bandwidth charges)
		const _s3GatewayEndpoint = "S3GatewayEndpoint"
		template.Resources[_s3GatewayEndpoint] = &ec2.VPCEndpoint{
			VpcEndpointType: ptr.String("Gateway"),
			VpcId:           cloudformation.Ref(_vpc),
			ServiceName:     cloudformation.Sub("com.amazonaws.${AWS::Region}.s3"),
		}

		template.Outputs[outputs.SubnetID] = cloudformation.Output{
			Value:       cloudformation.Ref(_subnet),
			Description: ptr.String("ID of the subnet"),
		}
	}

	const _securityGroup = "SecurityGroup"
	template.Resources[_securityGroup] = &ec2.SecurityGroup{
		Tags:             defaultTags, // Name tag is ignored
		GroupDescription: "Security group for the ECS task that allows all outbound and inbound traffic",
		VpcId:            vpcId, // FIXME: should be the VpcId of the given subnet
		SecurityGroupIngress: []ec2.SecurityGroup_Ingress{
			{
				IpProtocol: "tcp",
				FromPort:   ptr.Int(1),
				ToPort:     ptr.Int(65535),
				CidrIp:     ptr.String("0.0.0.0/0"), // from anywhere; FIXME: make optional and/or restrict to "my ip"
			},
		},
		// SecurityGroupEgress: []ec2.SecurityGroup_Egress{; FIXME: add ability to restrict outbound traffic
		// 	{
		// 		IpProtocol: "tcp",
		// 		FromPort:   ptr.Int(1),
		// 		ToPort:     ptr.Int(65535),
		// 		// CidrIp:     ptr.String("
		// 	},
		// },
	}

	// Declare stack outputs
	template.Outputs[outputs.TaskDefArn] = cloudformation.Output{
		Value:       cloudformation.Ref(_taskDefinition),
		Description: ptr.String("ARN of the ECS task definition"),
	}
	template.Outputs[outputs.ClusterName] = cloudformation.Output{
		Value:       cloudformation.Ref(_cluster),
		Description: ptr.String("Name of the ECS cluster"),
	}
	template.Outputs[outputs.LogGroupARN] = cloudformation.Output{
		Value:       cloudformation.GetAtt(_logGroup, "Arn"),
		Description: ptr.String("ARN of the CloudWatch log group"),
	}
	template.Outputs[outputs.SecurityGroupID] = cloudformation.Output{
		Value:       cloudformation.Ref(_securityGroup),
		Description: ptr.String("ID of the security group"),
	}
	template.Outputs[outputs.BucketName] = cloudformation.Output{
		Value:       cloudformation.Ref(_bucket),
		Description: ptr.String("Name of the S3 bucket"),
	}

	return template
}
