package deeds

import (
	"regexp"
)

// ResolveRegistrationType returns the portal labels for a deed type (as the
// AI extracts it), falling back to the default (gift deed) mapping.
func ResolveRegistrationType(deedType string) DeedTypeMapping {
	for _, m := range DeedTypePortalMapping {
		if m.Name == deedType {
			return m
		}
	}
	for _, m := range DeedTypePortalMapping {
		if m.Name == DefaultDeedType {
			return m
		}
	}
	return DeedTypePortalMapping[0]
}

func mappingFor(deedType string) (DeedTypeMapping, bool) {
	for _, m := range DeedTypePortalMapping {
		if m.Name == deedType {
			return m, true
		}
	}
	return DeedTypeMapping{}, false
}

// BookPortalLabel is the label the portal's Book dropdown shows.
func BookPortalLabel(book int) string {
	if l, ok := BookPortalLabels[book]; ok {
		return l
	}
	return BookPortalLabels[1]
}

// RegistrationLabels are a deed's portal Transaction Type / Sub Doc Type labels.
type RegistrationLabels struct{ RegistrationType, RegistrationSubType string }

// PortalRegistrationType is the portal's labels for a RegistrationDetail's
// stored values. The wizard stores the portal's own labels; deeds made before
// that (and every deed the AI pipeline fills in) may hold the everyday
// deed-type word, which is mapped here.
func PortalRegistrationType(deedType, deedSubType string) RegistrationLabels {
	if m, ok := mappingFor(deedType); ok {
		sub := deedSubType
		if sub == "" {
			sub = m.SubType
		}
		return RegistrationLabels{m.RegistrationType, sub}
	}
	if deedType == "" {
		m, _ := mappingFor(DefaultDeedType)
		return RegistrationLabels{m.RegistrationType, m.SubType}
	}
	return RegistrationLabels{deedType, deedSubType}
}

var subtypeCodeRe = regexp.MustCompile(`--\s*(\d{3,4})\s*$`)

// SubtypeTransactionCode is the numeric code ending a portal sub-type label
// ("...--0702" -> "0702").
func SubtypeTransactionCode(deedType, deedSubType string) string {
	sub := PortalRegistrationType(deedType, deedSubType).RegistrationSubType
	if m := subtypeCodeRe.FindStringSubmatch(sub); m != nil {
		return m[1]
	}
	return ""
}
