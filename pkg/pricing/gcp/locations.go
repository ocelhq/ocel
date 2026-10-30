package gcp

type location struct {
	continent      string
	cdnDestination string
	storage        string
	firestore      string
	memorystore    string
	sql            string
	network        string
	cloudRunTier2  bool
}

var locations = map[string]location{
	"africa-south1":           {continent: "Africa", cdnDestination: "Other", storage: "Johannesburg", firestore: "Johannesburg", memorystore: "Johannesburg", sql: "Johannesburg", network: "Johannesburg"},
	"asia-east1":              {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Asia Regional", firestore: "Taiwan", memorystore: "Taiwan", sql: "APAC", network: "Taiwan"},
	"asia-east2":              {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Hong Kong", firestore: "Hong Kong", memorystore: "Hong Kong", sql: "Hong Kong", network: "Hong Kong", cloudRunTier2: true},
	"asia-northeast1":         {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Tokyo", firestore: "Tokyo", memorystore: "Tokyo", sql: "Japan", network: "Tokyo"},
	"asia-northeast2":         {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Osaka", firestore: "Osaka", memorystore: "Osaka", sql: "Osaka", network: "Osaka"},
	"asia-northeast3":         {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Seoul", firestore: "Seoul", memorystore: "Seoul", sql: "Seoul", network: "Seoul", cloudRunTier2: true},
	"asia-south1":             {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Mumbai", firestore: "Mumbai", memorystore: "Mumbai", sql: "Mumbai", network: "Mumbai"},
	"asia-south2":             {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Delhi", firestore: "Delhi", memorystore: "Delhi", sql: "Delhi", network: "Delhi", cloudRunTier2: true},
	"asia-southeast1":         {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Singapore", firestore: "Singapore", memorystore: "Singapore", sql: "Singapore", network: "Singapore", cloudRunTier2: true},
	"asia-southeast2":         {continent: "AsiaPacific", cdnDestination: "Asia", storage: "Jakarta", firestore: "Jakarta", memorystore: "Jakarta", sql: "Jakarta", network: "Jakarta", cloudRunTier2: true},
	"asia-southeast3":         {storage: "Bangkok", memorystore: "Bangkok", sql: "Bangkok", network: "Bangkok"},
	"asia-southeast4":         {storage: "Kuala Lumpur", memorystore: "Kuala Lumpur", network: "Kuala Lumpur"},
	"australia-southeast1":    {continent: "Oceania", cdnDestination: "Oceania", storage: "Sydney", firestore: "Sydney", memorystore: "Sydney", sql: "Sydney", network: "Sydney", cloudRunTier2: true},
	"australia-southeast2":    {continent: "Oceania", cdnDestination: "Oceania", storage: "Melbourne", firestore: "Melbourne", memorystore: "Melbourne", sql: "Melbourne", network: "Melbourne", cloudRunTier2: true},
	"europe-central2":         {continent: "Europe", cdnDestination: "Europe", storage: "Warsaw", firestore: "Warsaw", memorystore: "Warsaw", sql: "Warsaw", network: "Warsaw", cloudRunTier2: true},
	"europe-north1":           {continent: "Europe", cdnDestination: "Europe", storage: "Finland", firestore: "Finland", memorystore: "Finland", sql: "Finland", network: "Finland"},
	"europe-north2":           {continent: "Europe", cdnDestination: "Europe", storage: "Stockholm", firestore: "Stockholm", memorystore: "Stockholm", sql: "Stockholm", network: "Stockholm"},
	"europe-southwest1":       {continent: "Europe", cdnDestination: "Europe", storage: "Madrid", firestore: "Madrid", memorystore: "Madrid", sql: "Madrid", network: "Madrid"},
	"europe-west1":            {continent: "Europe", cdnDestination: "Europe", storage: "Belgium", firestore: "Belgium", memorystore: "Belgium", sql: "EMEA", network: "Belgium"},
	"europe-west10":           {continent: "Europe", cdnDestination: "Europe", storage: "Berlin", firestore: "Berlin", memorystore: "Berlin", sql: "Berlin", network: "Berlin", cloudRunTier2: true},
	"europe-west12":           {continent: "Europe", cdnDestination: "Europe", storage: "Turin", firestore: "Turin", memorystore: "Turin", sql: "Turin", network: "Turin", cloudRunTier2: true},
	"europe-west2":            {continent: "Europe", cdnDestination: "Europe", storage: "London", firestore: "London", memorystore: "London", sql: "London", network: "London", cloudRunTier2: true},
	"europe-west3":            {continent: "Europe", cdnDestination: "Europe", storage: "Frankfurt", firestore: "Frankfurt", memorystore: "Frankfurt", sql: "Frankfurt", network: "Frankfurt", cloudRunTier2: true},
	"europe-west4":            {continent: "Europe", cdnDestination: "Europe", storage: "Netherlands", firestore: "Netherlands", memorystore: "Netherlands", sql: "Netherlands", network: "Netherlands"},
	"europe-west5":            {memorystore: "Galaxy Frankfurt", network: "Galaxy Frankfurt"},
	"europe-west6":            {continent: "Europe", cdnDestination: "Europe", storage: "Zurich", firestore: "Zurich", memorystore: "Zurich", sql: "Zurich", network: "Zurich", cloudRunTier2: true},
	"europe-west8":            {continent: "Europe", cdnDestination: "Europe", storage: "Milan", firestore: "Milan", memorystore: "Milan", sql: "Milan", network: "Milan"},
	"europe-west9":            {continent: "Europe", cdnDestination: "Europe", storage: "Paris", firestore: "Paris", memorystore: "Paris", sql: "Paris", network: "Paris"},
	"me-central1":             {continent: "MiddleEast", cdnDestination: "Other", storage: "Doha", firestore: "Doha", memorystore: "Doha", sql: "Doha", network: "Doha", cloudRunTier2: true},
	"me-central2":             {continent: "MiddleEast", cdnDestination: "Other", storage: "Dammam", firestore: "Dammam", memorystore: "Dammam", sql: "Dammam", network: "Dammam", cloudRunTier2: true},
	"me-west1":                {continent: "MiddleEast", cdnDestination: "Other", storage: "Tel Aviv", firestore: "Tel Aviv", memorystore: "TelAviv", sql: "Israel", network: "Tel Aviv"},
	"northamerica-northeast1": {continent: "North America", cdnDestination: "North America", storage: "Montreal", firestore: "Montreal", memorystore: "Montreal", sql: "Montreal", network: "Montréal", cloudRunTier2: true},
	"northamerica-northeast2": {continent: "North America", cdnDestination: "North America", storage: "Toronto", firestore: "Toronto", memorystore: "Toronto", sql: "Toronto", network: "Toronto", cloudRunTier2: true},
	"northamerica-south1":     {continent: "North America", cdnDestination: "North America", storage: "Mexico", memorystore: "Mexico", sql: "Mexico", network: "Mexico"},
	"southamerica-east1":      {continent: "South America", cdnDestination: "Latin America", storage: "Sao Paulo", firestore: "Sao Paulo", memorystore: "Sao Paulo", sql: "Sao Paulo", network: "São Paulo", cloudRunTier2: true},
	"southamerica-west1":      {continent: "South America", cdnDestination: "Latin America", storage: "Santiago", firestore: "Santiago", memorystore: "Santiago", sql: "Santiago", network: "Santiago", cloudRunTier2: true},
	"us-central1":             {continent: "North America", cdnDestination: "North America", storage: "US Regional", firestore: "Iowa", memorystore: "Iowa", sql: "Americas", network: "Iowa"},
	"us-central2":             {memorystore: "Oklahoma", sql: "Americas"},
	"us-east1":                {continent: "North America", cdnDestination: "North America", storage: "US Regional", firestore: "South Carolina", memorystore: "South Carolina", sql: "Americas", network: "South Carolina"},
	"us-east4":                {continent: "North America", cdnDestination: "North America", storage: "Northern Virginia", firestore: "Northern Virginia", memorystore: "Northern Virginia", sql: "Northern Virginia", network: "Northern Virginia"},
	"us-east5":                {continent: "North America", cdnDestination: "North America", storage: "Columbus", firestore: "Columbus", memorystore: "Columbus", sql: "Columbus", network: "Columbus"},
	"us-east7":                {continent: "North America", cdnDestination: "North America", storage: "Day0 (Alabama)", memorystore: "Alabama", sql: "Day0 (Alabama)", network: "Day0 (Alabama)"},
	"us-south1":               {continent: "North America", cdnDestination: "North America", storage: "Dallas", firestore: "Dallas", memorystore: "Dallas", sql: "Dallas", network: "Dallas"},
	"us-west1":                {continent: "North America", cdnDestination: "North America", storage: "US Regional", firestore: "Oregon", memorystore: "Oregon", sql: "Americas", network: "Oregon"},
	"us-west2":                {continent: "North America", cdnDestination: "North America", storage: "Los Angeles", firestore: "Los Angeles", memorystore: "Los Angeles", sql: "Los Angeles", network: "Los Angeles", cloudRunTier2: true},
	"us-west3":                {continent: "North America", cdnDestination: "North America", storage: "Salt Lake City", firestore: "Salt Lake City", memorystore: "Salt Lake City", sql: "Salt Lake City", network: "Salt Lake City", cloudRunTier2: true},
	"us-west4":                {continent: "North America", cdnDestination: "North America", storage: "Las Vegas", firestore: "Las Vegas", memorystore: "Las Vegas", sql: "Las Vegas", network: "Las Vegas", cloudRunTier2: true},
	"us-west8":                {continent: "North America", cdnDestination: "North America", storage: "Phoenix", firestore: "Phoenix", memorystore: "Phoenix", sql: "Phoenix", network: "Phoenix"},
}
