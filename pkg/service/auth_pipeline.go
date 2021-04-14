package service

import (
	"sync"

	"github.com/3scale-labs/authorino/pkg/common"
	"github.com/3scale-labs/authorino/pkg/config"
	envoy_auth "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"golang.org/x/net/context"
	ctrl "sigs.k8s.io/controller-runtime"
)

var (
	authCtxLog = ctrl.Log.WithName("Authorino").WithName("AuthPipeline")
)

type EvaluationResponse struct {
	Evaluator common.AuthConfigEvaluator
	Object    interface{}
	Error     error
}

func (evresp *EvaluationResponse) Success() bool {
	return evresp.Error == nil
}

func newEvaluationResponse(evaluator common.AuthConfigEvaluator, obj interface{}, err error) EvaluationResponse {
	return EvaluationResponse{
		Evaluator: evaluator,
		Object:    obj,
		Error:     err,
	}
}

// AuthPipeline evaluates the context of an auth request upon the auth configs defined for the requested API
// Throughout the pipeline, user identity, adhoc metadata and authorization policies are evaluated and their
// corresponding resulting objects stored in the respective maps.
type AuthPipeline struct {
	ParentContext *context.Context
	Request       *envoy_auth.CheckRequest
	API           *config.APIConfig

	Identity      map[*config.IdentityConfig]interface{}
	Metadata      map[*config.MetadataConfig]interface{}
	Authorization map[*config.AuthorizationConfig]interface{}
}

// NewAuthPipeline creates an AuthPipeline instance
func NewAuthPipeline(parentCtx context.Context, req *envoy_auth.CheckRequest, apiConfig config.APIConfig) AuthPipeline {
	return AuthPipeline{
		ParentContext: &parentCtx,
		Request:       req,
		API:           &apiConfig,
		Identity:      make(map[*config.IdentityConfig]interface{}),
		Metadata:      make(map[*config.MetadataConfig]interface{}),
		Authorization: make(map[*config.AuthorizationConfig]interface{}),
	}
}

func (pipeline *AuthPipeline) evaluateAuthConfig(config common.AuthConfigEvaluator, ctx context.Context, respChannel *chan EvaluationResponse, successCallback func(), failureCallback func()) {
	if err := common.CheckContext(ctx); err != nil {
		authCtxLog.Info("Skipping auth config", "config", config, "reason", err)
		return
	}

	if authObj, err := config.Call(pipeline, ctx); err != nil {
		*respChannel <- newEvaluationResponse(config, nil, err)

		authCtxLog.Info("Failed to evaluate auth object", "config", config, "error", err)

		if failureCallback != nil {
			failureCallback()
		}
	} else {
		*respChannel <- newEvaluationResponse(config, authObj, nil)

		if successCallback != nil {
			successCallback()
		}
	}
}

type authConfigEvaluationStrategy func(conf common.AuthConfigEvaluator, ctx context.Context, respChannel *chan EvaluationResponse, cancel func())

func (pipeline *AuthPipeline) evaluateAuthConfigs(authConfigs []common.AuthConfigEvaluator, respChannel *chan EvaluationResponse, es authConfigEvaluationStrategy) {
	ctx, cancel := context.WithCancel(*pipeline.ParentContext)
	waitGroup := new(sync.WaitGroup)
	waitGroup.Add(len(authConfigs))

	for _, authConfig := range authConfigs {
		objConfig := authConfig
		go func() {
			defer waitGroup.Done()

			es(objConfig, ctx, respChannel, cancel)
		}()
	}

	waitGroup.Wait()
}

func (pipeline *AuthPipeline) evaluateOneAuthConfig(authConfigs []common.AuthConfigEvaluator, respChannel *chan EvaluationResponse) {
	pipeline.evaluateAuthConfigs(authConfigs, respChannel, func(conf common.AuthConfigEvaluator, ctx context.Context, respChannel *chan EvaluationResponse, cancel func()) {
		pipeline.evaluateAuthConfig(conf, ctx, respChannel, cancel, nil) // cancels the context if at least one thread succeeds
	})
}

func (pipeline *AuthPipeline) evaluateAllAuthConfigs(authConfigs []common.AuthConfigEvaluator, respChannel *chan EvaluationResponse) {
	pipeline.evaluateAuthConfigs(authConfigs, respChannel, func(conf common.AuthConfigEvaluator, ctx context.Context, respChannel *chan EvaluationResponse, cancel func()) {
		pipeline.evaluateAuthConfig(conf, ctx, respChannel, nil, cancel) // cancels the context if at least one thread fails
	})
}

func (pipeline *AuthPipeline) evaluateAnyAuthConfig(authConfigs []common.AuthConfigEvaluator, respChannel *chan EvaluationResponse) {
	pipeline.evaluateAuthConfigs(authConfigs, respChannel, func(conf common.AuthConfigEvaluator, ctx context.Context, respChannel *chan EvaluationResponse, _ func()) {
		pipeline.evaluateAuthConfig(conf, ctx, respChannel, nil, nil)
	})
}

func (pipeline *AuthPipeline) evaluateIdentityConfigs() error {
	configs := pipeline.API.IdentityConfigs
	respChannel := make(chan EvaluationResponse, len(configs))

	go func() {
		defer close(respChannel)
		pipeline.evaluateOneAuthConfig(configs, &respChannel)
	}()

	var lastError error

	for resp := range respChannel {
		conf, _ := resp.Evaluator.(*config.IdentityConfig)
		obj := resp.Object

		if resp.Success() {
			pipeline.Identity[conf] = obj
			authCtxLog.Info("Identity", "config", conf, "authObj", obj)
			return nil
		} else {
			lastError = resp.Error
			authCtxLog.Info("Identity", "config", conf, "error", lastError)
		}
	}

	return lastError
}

func (pipeline *AuthPipeline) evaluateMetadataConfigs() {
	configs := pipeline.API.MetadataConfigs
	respChannel := make(chan EvaluationResponse, len(configs))

	go func() {
		defer close(respChannel)
		pipeline.evaluateAnyAuthConfig(configs, &respChannel)
	}()

	for resp := range respChannel {
		conf, _ := resp.Evaluator.(*config.MetadataConfig)
		obj := resp.Object

		if resp.Success() {
			pipeline.Metadata[conf] = obj
			authCtxLog.Info("Metadata", "config", conf, "authObj", obj)
		} else {
			authCtxLog.Info("Metadata", "config", conf, "error", resp.Error)
		}
	}
}

func (pipeline *AuthPipeline) evaluateAuthorizationConfigs() error {
	configs := pipeline.API.AuthorizationConfigs
	respChannel := make(chan EvaluationResponse, len(configs))

	go func() {
		defer close(respChannel)
		pipeline.evaluateAllAuthConfigs(configs, &respChannel)
	}()

	for resp := range respChannel {
		conf, _ := resp.Evaluator.(*config.AuthorizationConfig)
		obj := resp.Object

		if resp.Success() {
			pipeline.Authorization[conf] = obj
			authCtxLog.Info("Authorization", "config", conf, "authObj", obj)
		} else {
			err := resp.Error
			authCtxLog.Info("Authorization", "config", conf, "error", err)
			return err
		}
	}

	return nil
}

// Evaluate evaluates all steps of the auth pipeline (identity → metadata → policy enforcement)
func (pipeline *AuthPipeline) Evaluate() error {
	// identity
	if err := pipeline.evaluateIdentityConfigs(); err != nil {
		return err
	}

	// metadata
	pipeline.evaluateMetadataConfigs()

	// policy enforcement (authorization)
	if err := pipeline.evaluateAuthorizationConfigs(); err != nil {
		return err
	}

	return nil
}

func (pipeline *AuthPipeline) GetParentContext() *context.Context {
	return pipeline.ParentContext
}

func (pipeline *AuthPipeline) GetRequest() *envoy_auth.CheckRequest {
	return pipeline.Request
}

func (pipeline *AuthPipeline) GetHttp() *envoy_auth.AttributeContext_HttpRequest {
	return pipeline.Request.Attributes.Request.Http
}

func (pipeline *AuthPipeline) GetAPI() interface{} {
	return pipeline.API
}

func (pipeline *AuthPipeline) GetResolvedIdentity() (interface{}, interface{}) {
	for identityConfig, identityObj := range pipeline.Identity {
		if identityObj != nil {
			id := identityConfig
			obj := identityObj
			return id, obj
		}
	}
	return nil, nil
}

func (pipeline *AuthPipeline) GetResolvedMetadata() map[interface{}]interface{} {
	m := make(map[interface{}]interface{})
	for metadataCfg, metadataObj := range pipeline.Metadata {
		if metadataObj != nil {
			m[metadataCfg] = metadataObj
		}
	}
	return m
}

func (pipeline *AuthPipeline) GetDataForAuthorization() interface{} {
	authData := make(map[string]interface{})
	_, authData["identity"] = pipeline.GetResolvedIdentity()

	resolvedMetadata := make(map[string]interface{})
	for config, obj := range pipeline.GetResolvedMetadata() {
		metadataConfig, _ := config.(common.NamedConfigEvaluator)
		resolvedMetadata[metadataConfig.GetName()] = obj
	}
	authData["metadata"] = resolvedMetadata

	type authorizationData struct {
		Context  *envoy_auth.AttributeContext `json:"context"`
		AuthData map[string]interface{}       `json:"auth"`
	}

	return &authorizationData{
		Context:  pipeline.GetRequest().Attributes,
		AuthData: authData,
	}
}