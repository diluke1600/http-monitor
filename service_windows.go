//go:build windows

package main

import (
	"context"
	"flag"
	"log"
	"time"

	"github.com/kardianos/service"
)

var serviceCommand = flag.String("service", "", "控制 Windows 服务: install|uninstall|start|stop|run")

type winProgram struct {
	run    func(context.Context)
	cancel context.CancelFunc
}

func (p *winProgram) Start(s service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go p.run(ctx)
	return nil
}

func (p *winProgram) Stop(s service.Service) error {
	if p.cancel != nil {
		p.cancel()
	}
	time.Sleep(2 * time.Second)
	return nil
}

func handleWindowsService(run func(context.Context)) bool {
	cfg := &service.Config{
		Name:        "HttpMonitor",
		DisplayName: "HTTP Monitor",
		Description: "Monitors HTTP endpoints and sends Feishu alerts.",
	}

	prg := &winProgram{run: run}
	s, err := service.New(prg, cfg)
	if err != nil {
		log.Fatalf("创建 Windows 服务失败: %v", err)
	}

	// 有 -service 子命令时，作为命令行工具控制服务
	if serviceCommand != nil && *serviceCommand != "" {
		switch *serviceCommand {
		case "install", "uninstall", "start", "stop":
			if err := service.Control(s, *serviceCommand); err != nil {
				log.Fatalf("执行服务命令 %s 失败: %v", *serviceCommand, err)
			}
			log.Printf("服务命令 %s 执行成功\n", *serviceCommand)
			return true
		case "run":
			if err := s.Run(); err != nil {
				log.Fatalf("以服务模式运行失败: %v", err)
			}
			return true
		default:
			log.Fatalf("未知 service 命令: %s", *serviceCommand)
		}
	}

	// 没有 -service 参数：
	// - 如果在交互模式（控制台运行），走普通 main 流程
	// - 如果被 Windows 服务管理器启动（非交互），必须走 s.Run()
	if !service.Interactive() {
		if err := s.Run(); err != nil {
			log.Fatalf("作为 Windows 服务运行失败: %v", err)
		}
		return true
	}

	return false
}
