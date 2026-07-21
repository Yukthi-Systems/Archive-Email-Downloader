/*
 * Copyright (C) 2026 Yukthi Systems Private Limited
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3
 * as published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * version 3 along with this program. If not, see
 * <https://www.gnu.org/licenses/>.
 */

package db

import (
	"fmt"
	"strings"
)

func ParseLocation(loc string) (*ParsedLocation, error) {
	parts := strings.Split(loc, ":")
	if len(parts) != 5 {
		return nil, fmt.Errorf("invalid archive location format")
	}

	return &ParsedLocation{
		ServerIP: parts[0],
		OrgID:    parts[1],
		Domain:   parts[2],
		Date:     parts[3],
		ID:       parts[4],
	}, nil
}

// BuildURL constructs the API endpoint
func (p *ParsedLocation) BuildURL() string {
	return fmt.Sprintf("http://%s:8686/archive/read/%s/%s/%s/%s",
		p.ServerIP, p.OrgID, p.Domain, p.Date, p.ID)
}

func (p *ParsedLocation) GetOrgID() string {
	return p.OrgID
}
