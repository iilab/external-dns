package main

import (
	"fmt"
	"github.com/Sirupsen/logrus"
	"github.com/rancher/external-dns/providers"
	"github.com/rancher/go-rancher-metadata/metadata"
	"strings"
	"sync"
)

func UpdateDnsRecords(m metadata.Handler) error {
	metadataRecs, err := getMetadataDnsRecords(m)
	if err != nil {
		return fmt.Errorf("Error reading external dns entries: %v", err)
	}
	logrus.Debugf("DNS records from metadata: %v", metadataRecs)

	providerRecs, err := getProviderDnsRecords()
	if err != nil {
		return fmt.Errorf("Provider error reading dns entries: %v", err)
	}

	logrus.Debugf("DNS records from provider: %v", providerRecs)
	if err = addMissingRecords(metadataRecs, providerRecs); err != nil {
		return fmt.Errorf("Failed to add missing records: %v", err)
	}

	if err = removeExtraRecords(metadataRecs, providerRecs); err != nil {
		return fmt.Errorf("Failed to remove extra records: %v", err)
	}

	if err = updateExistingRecords(metadataRecs, providerRecs); err != nil {
		return fmt.Errorf("Failed to update existing records records: %v", err)
	}
	return nil
}

func addMissingRecords(metadataRecs map[string]providers.DnsRecord, providerRecs map[string]providers.DnsRecord) error {
	var toAdd []providers.DnsRecord
	for key := range metadataRecs {
		if _, ok := providerRecs[key]; !ok {
			toAdd = append(toAdd, metadataRecs[key])
		}
	}
	if len(toAdd) == 0 {
		logrus.Debug("No DNS records to add")
		return nil
	} else {
		logrus.Infof("DNS records to add: %v", toAdd)
	}
	return updateRecords(toAdd, &Add)
}

func updateRecords(toChange []providers.DnsRecord, op *Op) error {
	values := make(chan providers.DnsRecord)
	var wg sync.WaitGroup
	wg.Add(len(toChange))

	for _, value := range toChange {
		go func(value providers.DnsRecord) {
			defer wg.Done()
			values <- value
		}(value)
	}

	go func() error {
		for value := range values {
			switch *op {
			case Add:
				logrus.Infof("Adding dns record: %v", value)
				if err := provider.AddRecord(value); err != nil {
					return fmt.Errorf("Failed to add DNS record %v: %v", value, err)
				}
			case Remove:
				logrus.Infof("Removing dns record: %v", value)

				if err := provider.RemoveRecord(value); err != nil {
					return fmt.Errorf("Failed to remove DNS record %v: %v", value, err)
				}
			case Update:
				logrus.Infof("Updating dns record: %v", value)
				if err := provider.UpdateRecord(value); err != nil {
					return fmt.Errorf("Failed to update DNS record %v: %v", value, err)
				}
			}
		}
		return nil
	}()
	wg.Wait()
	return nil
}

func updateExistingRecords(metadataRecs map[string]providers.DnsRecord, providerRecs map[string]providers.DnsRecord) error {
	var toUpdate []providers.DnsRecord
	for key := range metadataRecs {
		if _, ok := providerRecs[key]; ok {
			metadataR := make(map[string]struct{}, len(metadataRecs[key].Records))
			for _, s := range metadataRecs[key].Records {
				metadataR[s] = struct{}{}
			}

			providerR := make(map[string]struct{}, len(providerRecs[key].Records))
			for _, s := range providerRecs[key].Records {
				providerR[s] = struct{}{}
			}
			var update bool
			if len(metadataR) != len(providerR) {
				update = true
			} else {
				for key := range metadataR {
					if _, ok := providerR[key]; !ok {
						update = true
					}
				}
				for key := range providerR {
					if _, ok := metadataR[key]; !ok {
						update = true
					}
				}
			}
			if update {
				toUpdate = append(toUpdate, metadataRecs[key])
			}
		}
	}

	if len(toUpdate) == 0 {
		logrus.Debug("No DNS records to update")
		return nil
	} else {
		logrus.Infof("DNS records to update: %v", toUpdate)
	}

	return updateRecords(toUpdate, &Update)
}

func removeExtraRecords(metadataRecs map[string]providers.DnsRecord, providerRecs map[string]providers.DnsRecord) error {
	var toRemove []providers.DnsRecord
	for key := range providerRecs {
		if _, ok := metadataRecs[key]; !ok {
			toRemove = append(toRemove, providerRecs[key])
		}
	}

	if len(toRemove) == 0 {
		logrus.Debug("No DNS records to remove")
		return nil
	} else {
		logrus.Infof("DNS records to remove: %v", toRemove)
	}
	return updateRecords(toRemove, &Remove)
}

func getProviderDnsRecords() (map[string]providers.DnsRecord, error) {
	allRecords, err := provider.GetRecords()
	if err != nil {
		return nil, err
	}
	ourRecords := make(map[string]providers.DnsRecord, len(allRecords))
	joins := []string{EnvironmentName, providers.RootDomainName}
	suffix := strings.ToLower(strings.Join(joins, "."))
	for _, value := range allRecords {
		if strings.HasSuffix(value.DomainName, suffix) {
			ourRecords[value.DomainName] = value
		}
	}
	return ourRecords, nil
}

func getMetadataDnsRecords(m metadata.Handler) (map[string]providers.DnsRecord, error) {
	dnsEntries := make(map[string]providers.DnsRecord)
	err := getContainersDnsRecords(m, dnsEntries, "", "")
	if err != nil {
		return dnsEntries, err
	}
	return dnsEntries, nil
}

func getContainersDnsRecords(m metadata.Handler, dnsEntries map[string]providers.DnsRecord, serviceName string, stackName string) error {
	containers, err := m.GetContainers()
	if err != nil {
		return err
	}

	for _, container := range containers {
		if len(container.ServiceName) == 0 {
			continue
		}

		if len(serviceName) != 0 {
			if serviceName != container.ServiceName {
				continue
			}
			if stackName != container.StackName {
				continue
			}
		}

		hostUUID := container.HostUUID
		if len(hostUUID) == 0 {
			logrus.Debugf("Container's %v host_uuid is empty", container.Name)
			continue
		}
		host, err := m.GetHost(hostUUID)
		if err != nil {
			logrus.Infof("%v", err)
			continue
		}
		ip := host.AgentIP
		domainNameEntries := []string{container.ServiceName, container.StackName, EnvironmentName, providers.RootDomainName}
		domainName := strings.ToLower(strings.Join(domainNameEntries, "."))
		records := []string{ip}
		dnsEntry := providers.DnsRecord{domainName, records, "A", 300}

		addToDnsEntries(m, dnsEntry, dnsEntries)
	}

	return nil
}

func addToDnsEntries(m metadata.Handler, dnsEntry providers.DnsRecord, dnsEntries map[string]providers.DnsRecord) {
	var records []string
	if _, ok := dnsEntries[dnsEntry.DomainName]; ok {
		records = dnsEntry.Records
	} else {
		records = dnsEntries[dnsEntry.DomainName].Records
		records = append(records, dnsEntry.Records...)
	}
	dnsEntry = providers.DnsRecord{dnsEntry.DomainName, records, "A", 300}
	dnsEntries[dnsEntry.DomainName] = dnsEntry
}
