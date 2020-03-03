package pki

import (
	"context"
	r "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/hashicorp/vault/logical"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type testEnv struct {
	Backend logical.Backend
	Context context.Context
	Storage logical.Storage
}

type venafiConfigString string

type testData struct {
	cert        string
	cn          string
	csrPK       []byte
	dns_email   string
	//dns_ip added to alt_names to support some old browsers which can't parse IP Addresses x509 extension
	dns_ip      string
	dns_ns      string
	//only_ip added IP Address x509 field
	only_ip     string
	keyPassword string
	private_key string
	provider    venafiConfigString
	signCSR     bool
	wrong_cert  string
	wrong_pkey  string
}

const (
	venafiConfigTPP             venafiConfigString = "TPP"
	venafiConfigTPPRestricted   venafiConfigString = "TPPRestricted"
	venafiConfigCloud           venafiConfigString = "Cloud"
	venafiConfigCloudRestricted venafiConfigString = "CloudRestricted"
	venafiConfigFake            venafiConfigString = "Fake"
)

var venafiTestTPPConfig = map[string]interface{}{
	"tpp_url":           os.Getenv("TPPURL"),
	"tpp_user":          os.Getenv("TPPUSER"),
	"tpp_password":      os.Getenv("TPPPASSWORD"),
	"zone":              os.Getenv("TPPZONE"),
	"trust_bundle_file": os.Getenv("TRUST_BUNDLE"),
}

var venafiTestTPPConfigRestricted = map[string]interface{}{
	"tpp_url":           os.Getenv("TPPURL"),
	"tpp_user":          os.Getenv("TPPUSER"),
	"tpp_password":      os.Getenv("TPPPASSWORD"),
	"zone":              os.Getenv("TPPZONE_RESTRICTED"),
	"trust_bundle_file": os.Getenv("TRUST_BUNDLE"),
}

var venafiTestCloudConfig = map[string]interface{}{
	"cloud_url": os.Getenv("CLOUDURL"),
	"apikey":    os.Getenv("CLOUDAPIKEY"),
	"zone":      os.Getenv("CLOUDZONE"),
}

var venafiTestCloudConfigRestricted = map[string]interface{}{
	"cloud_url": os.Getenv("CLOUDURL"),
	"apikey":    os.Getenv("CLOUDAPIKEY"),
	"zone":      os.Getenv("CLOUDRESTRICTEDZONE"),
}

var venafiTestFakeConfig = map[string]interface{}{
	"generate_lease": true,
	"fakemode":       true,
}

func (e *testEnv) IssueCertificate(t *testing.T, data testData, configString venafiConfigString) {

	roleData, roleName, err := makeConfig(configString)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := e.Backend.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "roles/" + roleName,
		Storage:   e.Storage,
		Data:      roleData,
	})

	if err != nil {
		t.Fatal(err)
	}

	if resp != nil && resp.IsError() {
		t.Fatalf("failed to create role, %#v", resp)
	}

	var issueData map[string]interface{}
	if data.keyPassword != "" {
		issueData = map[string]interface{}{
			"common_name": data.cn,
			"alt_names":   fmt.Sprintf("%s,%s, %s", data.dns_ns, data.dns_email, data.dns_ip),
			"ip_sans":     []string{data.only_ip},
			"key_password": data.keyPassword,
		}
	} else {
		issueData = map[string]interface{}{
			"common_name": data.cn,
			"alt_names":   fmt.Sprintf("%s,%s, %s", data.dns_ns, data.dns_email, data.dns_ip),
			"ip_sans":     []string{data.only_ip},
		}
	}


	resp, err = e.Backend.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "issue/" + roleName,
		Storage:   e.Storage,
		Data:      issueData,
	})

	if err != nil {
		t.Fatal(err)
	}

	if resp != nil && resp.IsError() {
		t.Fatalf("failed to issue certificate, %#v", resp.Data["error"])
	}

	if resp == nil {
		t.Fatalf("should be on output on issue certificate, but response is nil: %#v", resp)
	}

	data.cert = resp.Data["certificate"].(string)
	if data.keyPassword != "" {
		encryptedKey := resp.Data["private_key"].(string)
		b, _ := pem.Decode([]byte(encryptedKey))
		b.Bytes, err = x509.DecryptPEMBlock(b, []byte(data.keyPassword))
		if err != nil {
			t.Fatal(err)
		}
		data.private_key = string(pem.EncodeToMemory(b))
	} else {
		data.private_key = resp.Data["private_key"].(string)
	}

	data.provider = configString

	checkStandartCert(t, data)
}

func (e *testEnv) SignCertificate(t *testing.T, data testData, configString venafiConfigString) {

	roleData, roleName, err := makeConfig(configString)
	if err != nil {
		t.Fatal(err)
	}

	//Generating CSR for test
	certificateRequest := x509.CertificateRequest{}
	certificateRequest.Subject.CommonName = data.cn
	certificateRequest.DNSNames = append(certificateRequest.DNSNames, data.dns_ns, data.dns_ip)

	//Cloud odesn't support IP SANS
	if configString == venafiConfigTPP {
		certificateRequest.IPAddresses = []net.IP{net.ParseIP(data.dns_ip)}
	}

	org := os.Getenv("CERT_O")
	if org != "" {
		certificateRequest.Subject.Organization = append(certificateRequest.Subject.Organization, org)
	}

	//Generating pk for test
	priv, err := rsa.GenerateKey(r.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data.csrPK = pem.EncodeToMemory(
		&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(priv),
		},
	)

	csr, err := x509.CreateCertificateRequest(r.Reader, &certificateRequest, priv)
	if err != nil {
		csr = nil
	}
	pemCSR := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csr,
	})))

	resp, err := e.Backend.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "roles/" + roleName,
		Storage:   e.Storage,
		Data:      roleData,
	})

	if err != nil {
		t.Fatal(err)
	}

	if resp != nil && resp.IsError() {
		t.Fatalf("failed to create role, %#v", resp)
	}

	resp, err = e.Backend.HandleRequest(context.Background(), &logical.Request{
		Operation: logical.UpdateOperation,
		Path:      "sign/" + roleName,
		Storage:   e.Storage,
		Data: map[string]interface{}{
			"csr": pemCSR,
		},
	})

	if err != nil {
		t.Fatal(err)
	}

	if resp != nil && resp.IsError() {
		t.Fatalf("failed to issue certificate, %#v", resp.Data["error"])
	}

	if resp == nil {
		t.Fatalf("should be on output on issue certificate, but response is nil: %#v", resp)
	}

	if resp.Data["certificate"] == "" {
		t.Fatalf("expected a cert to be generated")
	}

	data.cert = resp.Data["certificate"].(string)
	data.provider = configString

	checkStandartCert(t, data)
}

func makeConfig(configString venafiConfigString) (roleData map[string]interface{}, roleName string, err error) {

	switch configString {
	case venafiConfigFake:
		roleData = venafiTestFakeConfig
		roleName = "fake-role"
	case venafiConfigTPP:
		roleData = venafiTestTPPConfig
		roleName = "tpp-role"
	case venafiConfigTPPRestricted:
		roleData = venafiTestTPPConfigRestricted
		roleName = "tpp-role-restricted"
	case venafiConfigCloud:
		roleData = venafiTestCloudConfig
		roleName = "cloud-role"
	case venafiConfigCloudRestricted:
		roleData = venafiTestCloudConfigRestricted
		roleName = "cloud-role-restricted"
	default:
		return roleData, roleName, fmt.Errorf("Don't have config data for config %s", configString)
	}

	return roleData, roleName, nil

}

func (e *testEnv) FakeIssueCertificate(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "venafi.example.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.dns_ip = "192.168.1.1"
	data.only_ip = "127.0.0.1"
	data.dns_email = "venafi@example.com"

	var config = venafiConfigFake
	e.IssueCertificate(t, data, config)

}

func (e *testEnv) FakeIssueCertificateWithPassword(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "venafi.example.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.dns_ip = "192.168.1.1"
	data.only_ip = "127.0.0.1"
	data.dns_email = "venafi@example.com"
	data.keyPassword = "password"

	var config = venafiConfigFake
	e.IssueCertificate(t, data, config)

}

func (e *testEnv) FakeSignCertificate(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "venafi.example.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.dns_ip = "192.168.1.1"
	data.only_ip = "127.0.0.1"
	data.dns_email = "venafi@example.com"
	data.keyPassword = "password"
	data.signCSR = true

	var config = venafiConfigFake
	e.SignCertificate(t, data, config)
}

func (e *testEnv) TPPIssueCertificate(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "venafi.example.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.dns_ip = "192.168.1.1"
	data.dns_email = "venafi@example.com"

	var config = venafiConfigTPP
	e.IssueCertificate(t, data, config)

}

func (e *testEnv) TPPIssueCertificateWithPassword(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "venafi.example.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.dns_ip = "192.168.1.1"
	data.dns_email = "venafi@example.com"
	data.keyPassword = "Pass0rd!"

	var config = venafiConfigTPP
	e.IssueCertificate(t, data, config)

}

func (e *testEnv) TPPIssueCertificateRestricted(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "vfidev.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.dns_ip = "192.168.1.1"
	data.dns_email = "venafi@example.com"

	var config = venafiConfigTPPRestricted
	e.IssueCertificate(t, data, config)

}

func (e *testEnv) TPPSignCertificate(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "vfidev.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.dns_ip = "127.0.0.1"
	data.signCSR = true

	var config = venafiConfigTPP
	e.SignCertificate(t, data, config)

}

func (e *testEnv) CloudSignCertificate(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "venafi.example.com"
	data.cn = rand + "." + domain
	data.dns_ns = "alt-" + data.cn
	data.signCSR = true

	var config = venafiConfigCloud
	e.SignCertificate(t, data, config)

}

func (e *testEnv) CloudIssueCertificate(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "venafi.example.com"
	data.cn = rand + "." + domain

	var config = venafiConfigCloud
	e.IssueCertificate(t, data, config)
}

func (e *testEnv) CloudIssueCertificateRestricted(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "vfidev.com"
	data.cn = rand + "." + domain

	var config = venafiConfigCloud
	e.IssueCertificate(t, data, config)
}

func (e *testEnv) CloudIssueCertificateWithPassword(t *testing.T) {

	data := testData{}
	rand := randSeq(9)
	domain := "vfidev.com"
	data.cn = rand + "." + domain
	data.keyPassword = "password"

	var config = venafiConfigCloud
	e.IssueCertificate(t, data, config)
}

func checkStandartCert(t *testing.T, data testData) {
	var err error
	log.Println("Testing certificate:", data.cert)
	certPEMBlock, _ := pem.Decode([]byte(data.cert))
	if certPEMBlock == nil {
		t.Fatalf("Certificate data is nil in the pem block")
	}

	if !data.signCSR {
		log.Println("Testing private key:", data.private_key)
		keyPEMBlock, _ := pem.Decode([]byte(data.private_key))
		if keyPEMBlock == nil {
			t.Fatalf("Private key data is nil in thew private key")
		}
		_, err = tls.X509KeyPair([]byte(data.cert), []byte(data.private_key))
		if err != nil {
			t.Fatalf("Error parsing certificate key pair: %s", err)
		}
	} else {
		_, err = tls.X509KeyPair([]byte(data.cert), []byte(data.csrPK))
		if err != nil {
			t.Fatalf("Error parsing certificate key pair: %s", err)
		}
	}

	parsedCertificate, err := x509.ParseCertificate(certPEMBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	if parsedCertificate.Subject.CommonName != data.cn {
		t.Fatalf("Certificate common name expected to be %s but actualy it is %s", parsedCertificate.Subject.CommonName, data.cn)
	}

	//TODO: cloud now have SAN support too. Have to implement it

	wantDNSNames := []string{data.cn, data.dns_ns}
	if data.dns_ip != "" {
		wantDNSNames = append(wantDNSNames, data.dns_ip)
	}
	ips := make([]net.IP, 0, 2)
	if data.only_ip != "" {
		ips = append(ips, net.ParseIP(data.only_ip))
	}
	if !SameStringSlice(parsedCertificate.DNSNames, wantDNSNames) {
		t.Fatalf("Certificate Subject Alternative Names %v doesn't match to requested %v", parsedCertificate.DNSNames, wantDNSNames)
	}

	if !SameIpSlice(ips, parsedCertificate.IPAddresses) {
		t.Fatalf("Certificate IPs %v doesn`t match requested %v", parsedCertificate.IPAddresses, ips)
	}
	wantEmail := []string{data.dns_email}
	if !SameStringSlice(parsedCertificate.EmailAddresses, wantEmail) {
		t.Fatalf("Certificate emails %v doesn't match requested %v", parsedCertificate.EmailAddresses, wantEmail)
	}
	//TODO: in policies branch Cloud endpoint should start to populate O,C,L.. fields too
	wantOrg := os.Getenv("CERT_O")
	if wantOrg != "" {
		var haveOrg string
		if len(parsedCertificate.Subject.Organization) > 0 {
			haveOrg = parsedCertificate.Subject.Organization[0]
		} else {
			t.Fatalf("Organization in certificate is empty.")
		}
		log.Println("want and have", wantOrg, haveOrg)
		if wantOrg != haveOrg {
			t.Fatalf("Certificate Organization %s doesn't match to requested %s", haveOrg, wantOrg)
		}
	}

}
func newIntegrationTestEnv(t *testing.T) (*testEnv, error) {
	ctx := context.Background()
	defaultLeaseTTLVal := time.Hour * 24
	maxLeaseTTLVal := time.Hour * 24 * 32

	b, err := Factory(context.Background(), &logical.BackendConfig{
		Logger: nil,
		System: &logical.StaticSystemView{
			DefaultLeaseTTLVal: defaultLeaseTTLVal,
			MaxLeaseTTLVal:     maxLeaseTTLVal,
		},
	})
	if err != nil {
		return nil, err
	}
	return &testEnv{
		Backend: b,
		Context: ctx,
		Storage: &logical.InmemStorage{},
	}, nil
}
