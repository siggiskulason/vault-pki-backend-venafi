package pki

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/Venafi/vcert"
	"github.com/Venafi/vcert/pkg/endpoint"
	"github.com/Venafi/vcert/pkg/venafi/tpp"
	"github.com/hashicorp/vault/sdk/logical"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

func sliceContains(slice []string, item string) bool {
	set := make(map[string]struct{}, len(slice))
	for _, s := range slice {
		set[s] = struct{}{}
	}

	_, ok := set[item]
	return ok
}

func getHexFormatted(buf []byte, sep string) (string, error) {
	var ret bytes.Buffer
	for _, cur := range buf {
		if ret.Len() > 0 {
			if _, err := fmt.Fprint(&ret, sep); err != nil {
				return "", err
			}
		}
		if _, err := fmt.Fprintf(&ret, "%02x", cur); err != nil {
			return "", err
		}
	}
	return ret.String(), nil
}

func normalizeSerial(serial string) string {
	return strings.Replace(strings.ToLower(serial), ":", "-", -1)
}

type RunContext struct {
	TPPurl              string
	TPPuser             string
	TPPPassword         string
	TPPZone             string
	CloudUrl            string
	CloudAPIkey         string
	CloudZone           string
	TokenUrl            string
	AccessToken         string
	TPPTestingEnabled   bool
	CloudTestingEnabled bool
	FakeTestingEnabled  bool
	TokenTestingEnabled bool
	TPPIssuerCN         string
	CloudIssuerCN       string
	FakeIssuerCN        string
}

func GetContext() *RunContext {

	c := RunContext{}

	c.TPPurl = os.Getenv("TPP_URL")
	c.TPPuser = os.Getenv("TPP_USER")
	c.TPPPassword = os.Getenv("TPP_PASSWORD")
	c.TPPZone = os.Getenv("TPP_ZONE")

	c.CloudUrl = os.Getenv("CLOUD_URL")
	c.CloudAPIkey = os.Getenv("CLOUD_APIKEY")
	c.CloudZone = os.Getenv("CLOUD_ZONE")

	c.TokenUrl = os.Getenv("TPP_TOKEN_URL")
	c.AccessToken = os.Getenv("ACCESS_TOKEN")

	c.TPPTestingEnabled, _ = strconv.ParseBool(os.Getenv("VENAFI_TPP_TESTING"))
	c.CloudTestingEnabled, _ = strconv.ParseBool(os.Getenv("VENAFI_CLOUD_TESTING"))
	c.FakeTestingEnabled, _ = strconv.ParseBool(os.Getenv("VENAFI_FAKE_TESTING"))
	c.TokenTestingEnabled, _ = strconv.ParseBool(os.Getenv("VENAFI_TPP_TOKEN_TESTING"))

	c.TPPIssuerCN = os.Getenv("TPP_ISSUER_CN")
	c.CloudIssuerCN = os.Getenv("CLOUD_ISSUER_CN")
	c.FakeIssuerCN = os.Getenv("FAKE_ISSUER_CN")

	return &c
}

func SameIpSlice(x, y []net.IP) bool {
	if len(x) != len(y) {
		return false
	}
	x1 := make([]string, len(x))
	y1 := make([]string, len(y))
	for i := range x {
		x1[i] = x[i].String()
		y1[i] = y[i].String()
	}
	sort.Strings(x1)
	sort.Strings(y1)
	for i := range x1 {
		if x1[i] != y1[i] {
			return false
		}
	}
	return true
}

func SameStringSlice(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	x1 := make([]string, len(x))
	y1 := make([]string, len(y))
	copy(x1, x)
	copy(y1, y)
	sort.Strings(x1)
	sort.Strings(y1)
	for i := range x1 {
		if x1[i] != y1[i] {
			return false
		}
	}
	return true
}

func areDNSNamesCorrect(actualAltNames []string, expectedCNNames []string, expectedAltNames []string) bool {

	//There is no cn names. Check expectedAltNames only. Is it possible?
	if len(expectedCNNames) == 0 {
		if len(actualAltNames) != len(expectedAltNames) {
			return false

		} else if !SameStringSlice(actualAltNames, expectedAltNames) {
			return false
		}
	} else {

		//Checking expectedAltNames are in actualAltNames
		if len(actualAltNames) < len(expectedAltNames) {
			return false
		}

		for i := range expectedAltNames {
			expectedName := expectedAltNames[i]
			found := false

			for j := range actualAltNames {

				if actualAltNames[j] == expectedName {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}

		//Checking expectedCNNames
		allNames := append(expectedAltNames, expectedCNNames...)
		for i := range actualAltNames {
			name := actualAltNames[i]
			found := false

			for j := range allNames {

				if allNames[j] == name {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}

	}

	return true
}

func getTppConnector(cfg *vcert.Config) (*tpp.Connector, error) {

	var connectionTrustBundle *x509.CertPool
	if cfg.ConnectionTrust != "" {
		connectionTrustBundle = x509.NewCertPool()
		if !connectionTrustBundle.AppendCertsFromPEM([]byte(cfg.ConnectionTrust)) {
			return nil, fmt.Errorf("failed to parse PEM trust bundle")
		}
	}
	tppConnector, err := tpp.NewConnector(cfg.BaseUrl, "", cfg.LogVerbose, connectionTrustBundle)
	if err != nil {
		return nil, fmt.Errorf("could not create TPP connector: %s", err)
	}

	return tppConnector, nil
}

func updateAccessToken(cfg *vcert.Config, b *backend, ctx context.Context, req *logical.Request, roleName string) error {
	tppConnector, _ := getTppConnector(cfg)

	httpClient, err := getHTTPClient(cfg.ConnectionTrust)
	if err != nil {
		return err
	}

	tppConnector.SetHTTPClient(httpClient)

	resp, err := tppConnector.RefreshAccessToken(&endpoint.Authentication{
		RefreshToken: cfg.Credentials.RefreshToken,
		ClientId:     "hashicorp-vault-by-venafi",
		Scope:        "certificate:manage,revoke",
	})
	if resp.Access_token != "" && resp.Refresh_token != "" {

		err := storeAccessData(b, ctx, req, roleName, resp)
		if err != nil {
			return err
		}

	} else {
		return err
	}
	return nil
}

func storeAccessData(b *backend, ctx context.Context, req *logical.Request, roleName string, resp tpp.OauthRefreshAccessTokenResponse) error {
	entry, err := b.getRole(ctx, req.Storage, roleName)

	if err != nil {
		return err
	}

	if entry.VenafiSecret == "" {
		return fmt.Errorf("Role " + roleName + " does not have any Venafi secret associated")
	}

	venafiEntry, err := b.getVenafiSecret(ctx, req.Storage, entry.VenafiSecret)
	if err != nil {
		return err
	}

	venafiEntry.AccessToken = resp.Access_token
	venafiEntry.RefreshToken = resp.Refresh_token

	// Store it
	jsonEntry, err := logical.StorageEntryJSON(CredentialsRootPath+entry.VenafiSecret, venafiEntry)
	if err != nil {
		return err
	}
	if err := req.Storage.Put(ctx, jsonEntry); err != nil {
		return err
	}
	return nil
}

func getHTTPClient(trustBundlePem string) (*http.Client, error) {

	var netTransport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	tlsConfig := http.DefaultTransport.(*http.Transport).TLSClientConfig
	/* #nosec */
	if trustBundlePem != "" {
		trustBundle, err := parseTrustBundlePEM(trustBundlePem)
		if err != nil {
			return nil, err
		}

		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		tlsConfig.RootCAs = trustBundle
	}

	tlsConfig.Renegotiation = tls.RenegotiateFreelyAsClient
	netTransport.TLSClientConfig = tlsConfig

	client := &http.Client{
		Timeout:   time.Second * 30,
		Transport: netTransport,
	}
	return client, nil
}

func parseTrustBundlePEM(trustBundlePem string) (*x509.CertPool, error) {
	var connectionTrustBundle *x509.CertPool

	if trustBundlePem != "" {
		connectionTrustBundle = x509.NewCertPool()
		if !connectionTrustBundle.AppendCertsFromPEM([]byte(trustBundlePem)) {
			return nil, fmt.Errorf("failed to parse PEM trust bundle")
		}
	} else {
		return nil, fmt.Errorf("trust bundle PEM data is empty")
	}

	return connectionTrustBundle, nil
}
