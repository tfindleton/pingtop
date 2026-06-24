package pingtop

func errorMessageForCategory(category string) string {
	if category == "ok" {
		return ""
	}
	return category
}

func testBoolPtr(value bool) *bool {
	return &value
}
