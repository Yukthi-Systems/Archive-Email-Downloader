/*
 * Copyright (C) 2026 Yukthi Systems Private Limited
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3
 * as published by the Free Software Foundation.
 *
 * This program is distributed in the hope  that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * version 3 along with this program. If not, see
 * <https://www.gnu.org/licenses/>.
 */

package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

type Logger struct {
	ErrorLog   *slog.Logger
	ProcessLog *slog.Logger
	GeneralLog *slog.Logger
}

func New(baseDir string) (*Logger, error) {
	subDirs := []string{"errors", "process", "general"}
	for _, d := range subDirs {
		if err := os.MkdirAll(filepath.Join(baseDir, d), 0755); err != nil {
			return nil, err
		}
	}

	createSlog := func(name string) (*slog.Logger, error) {
		f, err := os.OpenFile(filepath.Join(baseDir, name, "app.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, err
		}
		
		// MultiWriter outputs to both the file AND the terminal
		mw := io.MultiWriter(os.Stdout, f)
		
		// Use TextHandler so terminal output is highly readable
		handler := slog.NewTextHandler(mw, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		})
		
		return slog.New(handler), nil
	}

	errLog, _ := createSlog("errors")
	procLog, _ := createSlog("process")
	genLog, _ := createSlog("general")

	return &Logger{
		ErrorLog:   errLog,
		ProcessLog: procLog,
		GeneralLog: genLog,
	}, nil
}