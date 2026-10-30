package provider

// BindingFields selects declared account identity fields and expands defaults.
// Secret values never participate; a credential may supply a public app ID.
func BindingFields(d Descriptor, options map[string]string, secrets map[string][]byte) map[string]string {
	result := make(map[string]string)
	for _, o := range d.Options {
		if o.Identity {
			value := options[o.Key]
			if value == "" {
				value = o.Default
			}
			result[o.Key] = value
		}
	}
	for _, c := range d.Credentials {
		if c.Identity && !c.Secret {
			result[c.Key] = string(secrets[c.Key])
		}
	}
	return result
}
