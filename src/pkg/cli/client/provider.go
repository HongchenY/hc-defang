package client

import (
	"fmt"
)

type Provider string

const (
	ProviderAuto   Provider = "auto"
	ProviderDefang Provider = "defang"
	ProviderAWS    Provider = "aws"
	// ProviderAzure  Provider = "azure"
	// ProviderGCP    Provider = "gcp"
)

var allProviders = []Provider{
	ProviderAuto,
	ProviderDefang,
	ProviderAWS,
	// ProviderAzure,
	// ProviderGCP,
}

func (p Provider) String() string {
	return string(p)
}

func (p *Provider) Set(str string) error {
	for _, provider := range allProviders {
		if provider.String() == str {
			*p = provider
			return nil
		}
	}

	return fmt.Errorf("available providers are: %v", allProviders)
}

func (p Provider) Type() string {
	return "provider"
}