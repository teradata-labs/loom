import { useState, useEffect } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { Input } from '@/components/ui/input'
import { CheckCircle, XCircle, Activity, AlertCircle, TrendingUp, Play, Pause, RotateCcw } from 'lucide-react'
import ReactECharts from 'echarts-for-react'

export function StyleGuide() {
  // Interactive progress demo state
  const [demoProgress, setDemoProgress] = useState(45)
  const [isAnimating, setIsAnimating] = useState(false)

  useEffect(() => {
    if (!isAnimating) return
    const interval = setInterval(() => {
      setDemoProgress(prev => {
        if (prev >= 100) {
          setIsAnimating(false)
          return 100
        }
        return prev + 1
      })
    }, 50)
    return () => clearInterval(interval)
  }, [isAnimating])

  return (
    <div className="space-y-12 pb-20">
      {/* Hero */}
      <div className="space-y-4 animate-fade-in-up">
        <div className="flex items-center space-x-4">
          <h1 className="text-5xl font-bold tracking-tight font-mono">
            HAWK DESIGN SYSTEM
          </h1>
          <div className="status-dot success animate-pulse-slow" />
        </div>
        <p className="text-xl text-muted-foreground font-sans max-w-3xl">
          Technical Precision with Teradata Orange — A dark-first, data-centric design system
          for AI agent evaluation and telemetry. Glass morphism meets terminal aesthetics.
        </p>
      </div>

      {/* Color Palette */}
      <section className="space-y-6 animate-fade-in-up stagger-1">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">COLOR PALETTE</h2>
          <p className="text-muted-foreground font-sans">
            Teradata Orange accents on deep charcoal with semantic color coding
          </p>
        </div>

        {/* Primary Colors */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Primary Colors</CardTitle>
            <CardDescription>Core brand colors for the platform</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
              <div className="space-y-3">
                <div className="h-24 rounded-lg bg-primary glow-primary flex items-center justify-center">
                  <span className="font-mono text-sm text-white font-bold">#F37021</span>
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold text-primary">Primary</p>
                  <p className="text-xs text-muted-foreground">Teradata Orange</p>
                </div>
              </div>
              <div className="space-y-3">
                <div className="h-24 rounded-lg bg-background border border-border flex items-center justify-center">
                  <span className="font-mono text-sm text-foreground">#0d0d0d</span>
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold">Background</p>
                  <p className="text-xs text-muted-foreground">Deep Charcoal</p>
                </div>
              </div>
              <div className="space-y-3">
                <div className="h-24 rounded-lg bg-foreground flex items-center justify-center">
                  <span className="font-mono text-sm text-background">#f5f5f5</span>
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold">Foreground</p>
                  <p className="text-xs text-muted-foreground">Off-White</p>
                </div>
              </div>
              <div className="space-y-3">
                <div className="h-24 rounded-lg glass border border-border flex items-center justify-center">
                  <span className="font-mono text-sm">rgba(26,26,26,0.6)</span>
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold">Card</p>
                  <p className="text-xs text-muted-foreground">Glass Morphism</p>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Semantic Colors */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Semantic Colors</CardTitle>
            <CardDescription>Status and feedback colors</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
              <div className="space-y-3">
                <div className="h-24 rounded-lg flex items-center justify-center" style={{background: '#10b981', boxShadow: '0 0 20px rgba(16, 185, 129, 0.3)'}}>
                  <CheckCircle className="h-8 w-8 text-white" />
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold" style={{color: '#10b981'}}>Success</p>
                  <p className="text-xs text-muted-foreground">#10b981</p>
                </div>
              </div>
              <div className="space-y-3">
                <div className="h-24 rounded-lg bg-destructive glow-destructive flex items-center justify-center">
                  <XCircle className="h-8 w-8 text-white" />
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold text-destructive">Destructive</p>
                  <p className="text-xs text-muted-foreground">#ff4757</p>
                </div>
              </div>
              <div className="space-y-3">
                <div className="h-24 rounded-lg glow-warning flex items-center justify-center" style={{background: '#fbbf24'}}>
                  <AlertCircle className="h-8 w-8 text-black" />
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold" style={{color: '#fbbf24'}}>Warning</p>
                  <p className="text-xs text-muted-foreground">#fbbf24</p>
                </div>
              </div>
              <div className="space-y-3">
                <div className="h-24 rounded-lg flex items-center justify-center" style={{background: '#60a5fa', boxShadow: '0 0 20px rgba(96, 165, 250, 0.3)'}}>
                  <TrendingUp className="h-8 w-8 text-white" />
                </div>
                <div>
                  <p className="font-mono text-sm font-semibold" style={{color: '#60a5fa'}}>Info</p>
                  <p className="text-xs text-muted-foreground">#60a5fa</p>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Typography */}
      <section className="space-y-6 animate-fade-in-up stagger-2">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">TYPOGRAPHY</h2>
          <p className="text-muted-foreground font-sans">
            IBM Plex Mono for headings/data, Inter for body text
          </p>
        </div>

        <Card className="glass border-border/50">
          <CardContent className="pt-6 space-y-8">
            <div className="space-y-4">
              <div>
                <h1 className="text-5xl font-bold font-mono">Heading 1 / 48px</h1>
                <p className="text-xs text-muted-foreground mt-1">IBM Plex Mono Bold</p>
              </div>
              <div>
                <h2 className="text-4xl font-bold font-mono">Heading 2 / 36px</h2>
                <p className="text-xs text-muted-foreground mt-1">IBM Plex Mono Bold</p>
              </div>
              <div>
                <h3 className="text-3xl font-bold font-mono">Heading 3 / 30px</h3>
                <p className="text-xs text-muted-foreground mt-1">IBM Plex Mono Bold</p>
              </div>
              <div>
                <h4 className="text-2xl font-bold font-mono">Heading 4 / 24px</h4>
                <p className="text-xs text-muted-foreground mt-1">IBM Plex Mono Bold</p>
              </div>
            </div>

            <div className="border-t border-border/30 pt-6 space-y-4">
              <div>
                <p className="text-base font-sans">
                  Body text uses Inter for exceptional readability. It's clean, professional, and pairs
                  beautifully with the technical monospace headings. Regular weight at 16px.
                </p>
                <p className="text-xs text-muted-foreground mt-1">Inter Regular 16px</p>
              </div>
              <div>
                <p className="text-sm font-sans text-muted-foreground">
                  Small text for descriptions and secondary information. Inter Regular 14px.
                </p>
                <p className="text-xs text-muted-foreground mt-1">Inter Regular 14px</p>
              </div>
              <div>
                <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">
                  Micro Labels / 12px
                </p>
                <p className="text-xs text-muted-foreground mt-1">IBM Plex Mono 12px Uppercase</p>
              </div>
            </div>

            <div className="border-t border-border/30 pt-6">
              <div className="text-6xl font-bold metric-value text-primary">
                92,847
              </div>
              <p className="text-xs text-muted-foreground mt-2">
                Metric Display: IBM Plex Mono Bold 60px with tabular-nums
              </p>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Design Principles */}
      <section className="space-y-6 animate-fade-in-up stagger-3">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">DESIGN PRINCIPLES</h2>
          <p className="text-muted-foreground font-sans">
            Core concepts that define the HAWK aesthetic
          </p>
        </div>

        <Card className="glass border-border/50">
          <CardContent className="pt-6">
            <div className="grid md:grid-cols-3 gap-6">
              <div className="space-y-2">
                <h3 className="font-mono font-semibold text-primary">Glass Morphism</h3>
                <p className="text-sm text-muted-foreground font-sans">
                  Semi-transparent backgrounds with backdrop blur create depth and hierarchy.
                  All cards and surfaces use <code className="text-xs bg-background/50 px-1 py-0.5 rounded">.glass</code> class.
                </p>
              </div>
              <div className="space-y-2">
                <h3 className="font-mono font-semibold text-primary">Terminal Aesthetic</h3>
                <p className="text-sm text-muted-foreground font-sans">
                  Monospace typography for data and labels. Uppercase micro labels with tracking.
                  Status dots with glowing effects for live indicators.
                </p>
              </div>
              <div className="space-y-2">
                <h3 className="font-mono font-semibold text-primary">Purposeful Motion</h3>
                <p className="text-sm text-muted-foreground font-sans">
                  Staggered fade-in animations on page load. Hover effects with orange glow.
                  Smooth transitions create polish without distraction.
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Components */}
      <section className="space-y-6 animate-fade-in-up stagger-4">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">COMPONENTS</h2>
          <p className="text-muted-foreground font-sans">
            Reusable UI elements with consistent styling
          </p>
        </div>

        {/* Buttons */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Buttons</CardTitle>
            <CardDescription>Various button states and variants</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex flex-wrap gap-3">
              <Button>Default Button</Button>
              <Button variant="destructive">Destructive</Button>
              <Button variant="outline">Outline</Button>
              <Button variant="secondary">Secondary</Button>
              <Button variant="ghost">Ghost</Button>
              <Button disabled>Disabled</Button>
            </div>
            <div className="flex flex-wrap gap-3">
              <Button size="sm">Small</Button>
              <Button size="default">Default</Button>
              <Button size="lg">Large</Button>
            </div>
          </CardContent>
        </Card>

        {/* Badges */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Badges</CardTitle>
            <CardDescription>Status indicators and labels</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap gap-3">
              <Badge>Default</Badge>
              <Badge variant="secondary">Secondary</Badge>
              <Badge variant="destructive">Destructive</Badge>
              <Badge variant="outline">Outline</Badge>
              <Badge variant="success">Success</Badge>
              <Badge variant="warning">Warning</Badge>
            </div>
          </CardContent>
        </Card>

        {/* Status Dots */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Status Indicators</CardTitle>
            <CardDescription>Glowing status dots for live states</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="flex items-center space-x-3">
                <div className="status-dot success animate-pulse-slow" />
                <span className="font-sans">Success / Online</span>
              </div>
              <div className="flex items-center space-x-3">
                <div className="status-dot error" />
                <span className="font-sans">Error / Failed</span>
              </div>
              <div className="flex items-center space-x-3">
                <div className="status-dot warning" />
                <span className="font-sans">Warning / Pending</span>
              </div>
              <div className="flex items-center space-x-3">
                <div className="status-dot info" />
                <span className="font-sans">Info / Processing</span>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Progress Bars */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Progress Bars</CardTitle>
            <CardDescription>Terminal-grade loading and status indicators</CardDescription>
          </CardHeader>
          <CardContent className="space-y-8">
            {/* Standard Progress Bars */}
            <div className="space-y-4">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Standard Progress</p>
              <div className="space-y-2">
                <div className="flex justify-between text-sm">
                  <span className="font-mono text-muted-foreground">SUCCESS RATE</span>
                  <span className="font-mono font-semibold">87%</span>
                </div>
                <Progress value={87} />
              </div>
              <div className="space-y-2">
                <div className="flex justify-between text-sm">
                  <span className="font-mono text-muted-foreground">COMPLETION</span>
                  <span className="font-mono font-semibold">45%</span>
                </div>
                <Progress value={45} />
              </div>
            </div>

            {/* Semantic Color Progress Bars */}
            <div className="space-y-4 border-t border-border/30 pt-6">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Semantic States</p>

              {/* Success Progress */}
              <div className="space-y-2">
                <div className="flex justify-between items-center text-sm">
                  <div className="flex items-center space-x-2">
                    <CheckCircle className="h-4 w-4" style={{color: '#10b981'}} />
                    <span className="font-mono text-muted-foreground">SUCCESSFUL TESTS</span>
                  </div>
                  <span className="font-mono font-semibold" style={{color: '#10b981'}}>92%</span>
                </div>
                <div className="h-2 w-full rounded-full overflow-hidden" style={{background: 'rgba(16, 185, 129, 0.1)'}}>
                  <div
                    className="h-full rounded-full transition-all duration-500"
                    style={{
                      width: '92%',
                      background: '#10b981',
                      boxShadow: '0 0 10px rgba(16, 185, 129, 0.5)'
                    }}
                  />
                </div>
              </div>

              {/* Warning Progress */}
              <div className="space-y-2">
                <div className="flex justify-between items-center text-sm">
                  <div className="flex items-center space-x-2">
                    <AlertCircle className="h-4 w-4" style={{color: '#fbbf24'}} />
                    <span className="font-mono text-muted-foreground">RESOURCE USAGE</span>
                  </div>
                  <span className="font-mono font-semibold" style={{color: '#fbbf24'}}>73%</span>
                </div>
                <div className="h-2 w-full rounded-full overflow-hidden" style={{background: 'rgba(251, 191, 36, 0.1)'}}>
                  <div
                    className="h-full rounded-full transition-all duration-500"
                    style={{
                      width: '73%',
                      background: '#fbbf24',
                      boxShadow: '0 0 10px rgba(251, 191, 36, 0.5)'
                    }}
                  />
                </div>
              </div>

              {/* Error Progress */}
              <div className="space-y-2">
                <div className="flex justify-between items-center text-sm">
                  <div className="flex items-center space-x-2">
                    <XCircle className="h-4 w-4 text-destructive" />
                    <span className="font-mono text-muted-foreground">FAILED EXECUTIONS</span>
                  </div>
                  <span className="font-mono font-semibold text-destructive">8%</span>
                </div>
                <div className="h-2 w-full rounded-full overflow-hidden bg-destructive/10">
                  <div
                    className="h-full rounded-full glow-destructive transition-all duration-500"
                    style={{
                      width: '8%',
                      background: '#ff4757'
                    }}
                  />
                </div>
              </div>
            </div>

            {/* Striped Progress Bars */}
            <div className="space-y-4 border-t border-border/30 pt-6">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Striped & Animated</p>

              <div className="space-y-2">
                <div className="flex justify-between text-sm">
                  <span className="font-mono text-muted-foreground">PROCESSING</span>
                  <span className="font-mono font-semibold text-primary">64%</span>
                </div>
                <div className="h-3 w-full rounded-lg overflow-hidden bg-primary/10">
                  <div
                    className="h-full rounded-lg transition-all duration-500"
                    style={{
                      width: '64%',
                      background: 'repeating-linear-gradient(45deg, #F37021, #F37021 10px, rgba(243, 112, 33, 0.7) 10px, rgba(243, 112, 33, 0.7) 20px)',
                      boxShadow: '0 0 15px rgba(243, 112, 33, 0.4)',
                      animation: 'progress-shimmer 2s linear infinite'
                    }}
                  />
                </div>
              </div>

              <div className="space-y-2">
                <div className="flex justify-between text-sm">
                  <div className="flex items-center space-x-2">
                    <Activity className="h-4 w-4" style={{color: '#60a5fa'}} />
                    <span className="font-mono text-muted-foreground">STREAMING DATA</span>
                  </div>
                  <span className="font-mono font-semibold" style={{color: '#60a5fa'}}>∞</span>
                </div>
                <div className="h-3 w-full rounded-lg overflow-hidden" style={{background: 'rgba(96, 165, 250, 0.1)'}}>
                  <div
                    className="h-full rounded-lg"
                    style={{
                      width: '100%',
                      background: 'linear-gradient(90deg, transparent, rgba(96, 165, 250, 0.8), transparent)',
                      animation: 'progress-flow 2s ease-in-out infinite'
                    }}
                  />
                </div>
              </div>
            </div>

            {/* Multi-Segment Progress */}
            <div className="space-y-4 border-t border-border/30 pt-6">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Multi-Segment</p>

              <div className="space-y-3">
                <div className="text-xs font-mono text-muted-foreground">EVAL RUN DISTRIBUTION</div>
                <div className="flex h-4 w-full rounded-lg overflow-hidden" style={{background: 'rgba(255,255,255,0.05)'}}>
                  <div
                    className="flex items-center justify-center text-[10px] font-mono transition-all duration-500"
                    style={{width: '65%', background: '#10b981'}}
                    title="Pass: 65%"
                  >
                    65%
                  </div>
                  <div
                    className="flex items-center justify-center text-[10px] font-mono transition-all duration-500"
                    style={{width: '25%', background: '#fbbf24'}}
                    title="Partial: 25%"
                  >
                    25%
                  </div>
                  <div
                    className="flex items-center justify-center text-[10px] font-mono transition-all duration-500"
                    style={{width: '10%', background: '#ff4757'}}
                    title="Fail: 10%"
                  >
                    10%
                  </div>
                </div>
                <div className="flex justify-between text-xs">
                  <div className="flex items-center space-x-2">
                    <div className="h-2 w-2 rounded-full" style={{background: '#10b981'}} />
                    <span className="font-mono text-muted-foreground">Pass (65%)</span>
                  </div>
                  <div className="flex items-center space-x-2">
                    <div className="h-2 w-2 rounded-full" style={{background: '#fbbf24'}} />
                    <span className="font-mono text-muted-foreground">Partial (25%)</span>
                  </div>
                  <div className="flex items-center space-x-2">
                    <div className="h-2 w-2 rounded-full" style={{background: '#ff4757'}} />
                    <span className="font-mono text-muted-foreground">Fail (10%)</span>
                  </div>
                </div>
              </div>
            </div>

            {/* Circular Progress */}
            <div className="space-y-4 border-t border-border/30 pt-6">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Circular Progress</p>

              <div className="grid grid-cols-3 gap-6">
                {/* Success Circle */}
                <div className="flex flex-col items-center space-y-3">
                  <div className="relative w-24 h-24">
                    <svg className="transform -rotate-90" viewBox="0 0 100 100">
                      <circle
                        cx="50"
                        cy="50"
                        r="40"
                        fill="none"
                        stroke="rgba(16, 185, 129, 0.1)"
                        strokeWidth="8"
                      />
                      <circle
                        cx="50"
                        cy="50"
                        r="40"
                        fill="none"
                        stroke="#10b981"
                        strokeWidth="8"
                        strokeDasharray={`${2 * Math.PI * 40 * 0.87} ${2 * Math.PI * 40}`}
                        strokeLinecap="round"
                        style={{
                          filter: 'drop-shadow(0 0 8px rgba(16, 185, 129, 0.5))',
                          transition: 'stroke-dasharray 0.5s ease'
                        }}
                      />
                    </svg>
                    <div className="absolute inset-0 flex items-center justify-center">
                      <span className="text-xl font-bold font-mono" style={{color: '#10b981'}}>87</span>
                    </div>
                  </div>
                  <div className="text-xs font-mono text-muted-foreground">SUCCESS</div>
                </div>

                {/* Primary Circle */}
                <div className="flex flex-col items-center space-y-3">
                  <div className="relative w-24 h-24">
                    <svg className="transform -rotate-90" viewBox="0 0 100 100">
                      <circle
                        cx="50"
                        cy="50"
                        r="40"
                        fill="none"
                        stroke="rgba(243, 112, 33, 0.1)"
                        strokeWidth="8"
                      />
                      <circle
                        cx="50"
                        cy="50"
                        r="40"
                        fill="none"
                        stroke="#F37021"
                        strokeWidth="8"
                        strokeDasharray={`${2 * Math.PI * 40 * 0.64} ${2 * Math.PI * 40}`}
                        strokeLinecap="round"
                        className="glow-primary"
                        style={{
                          transition: 'stroke-dasharray 0.5s ease'
                        }}
                      />
                    </svg>
                    <div className="absolute inset-0 flex items-center justify-center">
                      <span className="text-xl font-bold font-mono text-primary">64</span>
                    </div>
                  </div>
                  <div className="text-xs font-mono text-muted-foreground">ACTIVE</div>
                </div>

                {/* Warning Circle */}
                <div className="flex flex-col items-center space-y-3">
                  <div className="relative w-24 h-24">
                    <svg className="transform -rotate-90" viewBox="0 0 100 100">
                      <circle
                        cx="50"
                        cy="50"
                        r="40"
                        fill="none"
                        stroke="rgba(251, 191, 36, 0.1)"
                        strokeWidth="8"
                      />
                      <circle
                        cx="50"
                        cy="50"
                        r="40"
                        fill="none"
                        stroke="#fbbf24"
                        strokeWidth="8"
                        strokeDasharray={`${2 * Math.PI * 40 * 0.43} ${2 * Math.PI * 40}`}
                        strokeLinecap="round"
                        style={{
                          filter: 'drop-shadow(0 0 8px rgba(251, 191, 36, 0.5))',
                          transition: 'stroke-dasharray 0.5s ease'
                        }}
                      />
                    </svg>
                    <div className="absolute inset-0 flex items-center justify-center">
                      <span className="text-xl font-bold font-mono" style={{color: '#fbbf24'}}>43</span>
                    </div>
                  </div>
                  <div className="text-xs font-mono text-muted-foreground">PENDING</div>
                </div>
              </div>
            </div>

            {/* Terminal-Style Progress */}
            <div className="space-y-4 border-t border-border/30 pt-6">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Terminal Style</p>

              <div className="space-y-3">
                <div className="space-y-2">
                  <div className="flex items-center space-x-3">
                    <span className="text-xs font-mono text-muted-foreground w-32">DOWNLOADING</span>
                    <div className="flex-1 font-mono text-xs" style={{color: '#10b981'}}>
                      {'[' + '█'.repeat(18) + '▒'.repeat(7) + ']'} 72%
                    </div>
                  </div>
                </div>

                <div className="space-y-2">
                  <div className="flex items-center space-x-3">
                    <span className="text-xs font-mono text-muted-foreground w-32">COMPILING</span>
                    <div className="flex-1 font-mono text-xs text-primary">
                      {'[' + '█'.repeat(22) + '▒'.repeat(3) + ']'} 88%
                    </div>
                  </div>
                </div>

                <div className="space-y-2">
                  <div className="flex items-center space-x-3">
                    <span className="text-xs font-mono text-muted-foreground w-32">INDEXING</span>
                    <div className="flex-1 font-mono text-xs" style={{color: '#60a5fa'}}>
                      {'[' + '█'.repeat(11) + '▒'.repeat(14) + ']'} 44%
                    </div>
                  </div>
                </div>
              </div>
            </div>

            {/* Interactive Demo */}
            <div className="space-y-4 border-t border-border/30 pt-6">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Interactive Demo</p>

              <div className="space-y-4 p-4 rounded-lg glass border border-primary/20">
                <div className="space-y-2">
                  <div className="flex justify-between items-center text-sm">
                    <span className="font-mono text-muted-foreground">EVAL EXECUTION</span>
                    <span className="font-mono font-semibold text-primary">{demoProgress}%</span>
                  </div>
                  <div className="h-3 w-full rounded-lg overflow-hidden bg-primary/10">
                    <div
                      className="h-full rounded-lg glow-primary transition-all duration-300"
                      style={{
                        width: `${demoProgress}%`,
                        background: 'linear-gradient(90deg, rgba(243, 112, 33, 0.8), #F37021)',
                      }}
                    />
                  </div>
                </div>

                <div className="flex items-center space-x-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setIsAnimating(!isAnimating)}
                    className="font-mono"
                  >
                    {isAnimating ? (
                      <>
                        <Pause className="h-3 w-3 mr-1" />
                        Pause
                      </>
                    ) : (
                      <>
                        <Play className="h-3 w-3 mr-1" />
                        Animate
                      </>
                    )}
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setDemoProgress(0)
                      setIsAnimating(false)
                    }}
                    className="font-mono"
                  >
                    <RotateCcw className="h-3 w-3 mr-1" />
                    Reset
                  </Button>
                  <span className="text-xs text-muted-foreground font-sans ml-auto">
                    Click to control progress
                  </span>
                </div>
              </div>
            </div>

            {/* Gradient Bars */}
            <div className="space-y-4 border-t border-border/30 pt-6">
              <p className="text-xs font-mono text-muted-foreground tracking-wider uppercase">Gradient Effects</p>

              <div className="space-y-2">
                <div className="flex justify-between text-sm">
                  <span className="font-mono text-muted-foreground">MODEL CONFIDENCE</span>
                  <span className="font-mono font-semibold text-primary">96%</span>
                </div>
                <div className="h-4 w-full rounded-full overflow-hidden" style={{background: 'rgba(255,255,255,0.05)'}}>
                  <div
                    className="h-full rounded-full relative overflow-hidden transition-all duration-500"
                    style={{
                      width: '96%',
                      background: 'linear-gradient(90deg, #10b981 0%, #fbbf24 50%, #F37021 100%)',
                      boxShadow: '0 0 20px rgba(243, 112, 33, 0.4)'
                    }}
                  >
                    <div
                      className="absolute inset-0 opacity-30"
                      style={{
                        background: 'linear-gradient(90deg, transparent, rgba(255,255,255,0.3), transparent)',
                        animation: 'progress-shine 2s ease-in-out infinite'
                      }}
                    />
                  </div>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        <style dangerouslySetInnerHTML={{__html: `
          @keyframes progress-shimmer {
            0% { background-position: 0 0; }
            100% { background-position: 40px 0; }
          }

          @keyframes progress-flow {
            0% { transform: translateX(-100%); }
            100% { transform: translateX(100%); }
          }

          @keyframes progress-shine {
            0% { transform: translateX(-100%); }
            100% { transform: translateX(200%); }
          }
        `}} />

        {/* Cards */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Cards</CardTitle>
            <CardDescription>Glass morphism containers with hover effects</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid md:grid-cols-2 gap-4">
              <Card className="card-hover glass border-border/50">
                <CardHeader>
                  <CardTitle className="font-mono text-base">Standard Card</CardTitle>
                  <CardDescription>With hover effect</CardDescription>
                </CardHeader>
                <CardContent>
                  <p className="text-sm text-muted-foreground">
                    Hover to see the lift and glow effect
                  </p>
                </CardContent>
              </Card>

              <Card className="card-hover glass border-primary/30 relative overflow-hidden">
                <div className="absolute inset-0 bg-gradient-to-br from-primary/5 to-transparent" />
                <CardHeader className="relative z-10">
                  <CardTitle className="font-mono text-base text-primary">Active Card</CardTitle>
                  <CardDescription>With gradient overlay</CardDescription>
                </CardHeader>
                <CardContent className="relative z-10">
                  <div className="flex items-center space-x-2">
                    <Activity className="h-4 w-4 text-primary animate-pulse" />
                    <p className="text-sm text-muted-foreground">
                      Running state with glow
                    </p>
                  </div>
                </CardContent>
              </Card>
            </div>
          </CardContent>
        </Card>

        {/* Input Fields */}
        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Input Fields</CardTitle>
            <CardDescription>Form inputs with focus states</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Input placeholder="Search evaluations..." />
            <Input placeholder="Filter by name..." />
            <Input placeholder="Disabled input" disabled />
          </CardContent>
        </Card>
      </section>

      {/* Animations */}
      <section className="space-y-6 animate-fade-in-up stagger-5">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">ANIMATIONS</h2>
          <p className="text-muted-foreground font-sans">
            Purposeful motion and micro-interactions
          </p>
        </div>

        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Animation Examples</CardTitle>
            <CardDescription>Hover and interact to see effects</CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            <div className="space-y-3">
              <p className="text-sm font-mono text-muted-foreground">FADE IN UP (Page load)</p>
              <div className="p-4 glass rounded-lg animate-fade-in-up">
                <p className="text-sm">Elements fade in and slide up on page load</p>
              </div>
            </div>

            <div className="space-y-3">
              <p className="text-sm font-mono text-muted-foreground">STAGGERED (Sequential delays)</p>
              <div className="space-y-2">
                <div className="p-3 glass rounded-lg animate-fade-in-up stagger-1">Item 1 (100ms delay)</div>
                <div className="p-3 glass rounded-lg animate-fade-in-up stagger-2">Item 2 (200ms delay)</div>
                <div className="p-3 glass rounded-lg animate-fade-in-up stagger-3">Item 3 (300ms delay)</div>
              </div>
            </div>

            <div className="space-y-3">
              <p className="text-sm font-mono text-muted-foreground">PULSE (Live indicators)</p>
              <div className="p-4 glass rounded-lg flex items-center space-x-4">
                <Activity className="h-6 w-6 text-primary animate-pulse-slow" />
                <span className="text-sm">Slow pulse for status indicators</span>
              </div>
            </div>

            <div className="space-y-3">
              <p className="text-sm font-mono text-muted-foreground">GLOW (Active states)</p>
              <div className="p-4 glass rounded-lg border-primary/30 animate-glow">
                <p className="text-sm">Subtle glow animation for active elements</p>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Metric Cards Example */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">METRIC CARDS</h2>
          <p className="text-muted-foreground font-sans">
            Data-first dashboard components
          </p>
        </div>

        <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-4">
          <Card className="card-hover glass border-border/50">
            <CardHeader className="pb-3">
              <CardTitle className="text-xs font-mono text-muted-foreground tracking-wider uppercase">
                Total Evaluations
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="text-4xl font-bold metric-value">1,247</div>
              <p className="text-xs text-muted-foreground mt-2 font-sans">
                Lifetime evaluations
              </p>
            </CardContent>
          </Card>

          <Card className="card-hover glass border-primary/20 relative overflow-hidden">
            <div className="absolute inset-0 bg-gradient-to-br from-primary/5 to-transparent" />
            <CardHeader className="pb-3 relative z-10">
              <CardTitle className="text-xs font-mono text-muted-foreground tracking-wider uppercase">
                Active Evals
              </CardTitle>
            </CardHeader>
            <CardContent className="relative z-10">
              <div className="flex items-baseline space-x-2">
                <div className="text-4xl font-bold metric-value text-primary">8</div>
                <Activity className="h-5 w-5 text-primary animate-pulse" />
              </div>
              <p className="text-xs text-muted-foreground mt-2 font-sans">
                Currently running
              </p>
            </CardContent>
          </Card>

          <Card className="card-hover glass border-primary/20 relative overflow-hidden">
            <div className="absolute inset-0 bg-gradient-to-br from-primary/5 to-transparent" />
            <CardHeader className="pb-3 relative z-10">
              <CardTitle className="text-xs font-mono text-muted-foreground tracking-wider uppercase">
                Success Rate
              </CardTitle>
            </CardHeader>
            <CardContent className="relative z-10">
              <div className="flex items-baseline space-x-1">
                <div className="text-4xl font-bold metric-value text-primary">87</div>
                <span className="text-xl font-mono text-primary">%</span>
              </div>
              <p className="text-xs text-muted-foreground mt-2 font-sans">
                Passing evaluations
              </p>
            </CardContent>
          </Card>

          <Card className="card-hover glass border-destructive/20 relative overflow-hidden">
            <div className="absolute inset-0 bg-gradient-to-br from-destructive/5 to-transparent" />
            <CardHeader className="pb-3 relative z-10">
              <CardTitle className="text-xs font-mono text-muted-foreground tracking-wider uppercase">
                Recent Failures
              </CardTitle>
            </CardHeader>
            <CardContent className="relative z-10">
              <div className="text-4xl font-bold metric-value text-destructive">3</div>
              <p className="text-xs text-muted-foreground mt-2 font-sans">
                Last 24 hours
              </p>
            </CardContent>
          </Card>
        </div>
      </section>

      {/* Data Visualizations - Sankey Diagram */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">DATA VISUALIZATIONS</h2>
          <p className="text-muted-foreground font-sans">
            Complex flow diagrams with Teradata Orange terminal aesthetic
          </p>
        </div>

        <Card className="glass border-border/50">
          <CardHeader>
            <CardTitle className="font-mono">Sankey Flow Diagram</CardTitle>
            <CardDescription>Eval execution pipeline with multi-stage flow visualization</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                EVAL EXECUTION FLOW - FROM START TO COMPLETION
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    tooltip: {
                      trigger: 'item',
                      triggerOn: 'mousemove',
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      formatter: (params: any) => {
                        if (params.dataType === 'edge') {
                          return `<div style="padding: 4px;">
                            <div style="color: #f37021; font-weight: bold;">${params.data.source} → ${params.data.target}</div>
                            <div style="color: #b5b5b5; margin-top: 4px;">Flow: ${params.data.value} executions</div>
                          </div>`
                        }
                        return `<div style="padding: 4px;">
                          <div style="color: #f37021; font-weight: bold;">${params.data.name}</div>
                          <div style="color: #b5b5b5; margin-top: 4px;">Stage: ${params.data.depth}</div>
                        </div>`
                      }
                    },
                    series: [
                      {
                        type: 'sankey',
                        layout: 'none',
                        emphasis: {
                          focus: 'adjacency',
                          lineStyle: {
                            color: '#f37021',
                            opacity: 0.8
                          }
                        },
                        lineStyle: {
                          color: 'gradient',
                          curveness: 0.5,
                          opacity: 0.4
                        },
                        itemStyle: {
                          borderWidth: 2,
                          borderColor: '#1a1a1a',
                          borderType: 'solid',
                          shadowBlur: 10,
                          shadowColor: 'rgba(243, 112, 33, 0.3)'
                        },
                        label: {
                          color: '#f5f5f5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 11,
                          fontWeight: 600,
                          formatter: '{b}',
                          position: 'right',
                          distance: 8
                        },
                        data: [
                          // Stage 0: Initial
                          { name: 'Eval Started', itemStyle: { color: '#f37021' }, depth: 0, x: '5%', y: '50%' },

                          // Stage 1: Entry points
                          { name: 'Query Parser', itemStyle: { color: '#60a5fa' }, depth: 1, x: '20%', y: '15%' },
                          { name: 'Tool Selector', itemStyle: { color: '#60a5fa' }, depth: 1, x: '20%', y: '50%' },
                          { name: 'Context Builder', itemStyle: { color: '#60a5fa' }, depth: 1, x: '20%', y: '85%' },

                          // Stage 2: Processing
                          { name: 'SQL Generation', itemStyle: { color: '#8b5cf6' }, depth: 2, x: '40%', y: '8%' },
                          { name: 'LLM Call', itemStyle: { color: '#8b5cf6' }, depth: 2, x: '40%', y: '28%' },
                          { name: 'Tool Execution', itemStyle: { color: '#8b5cf6' }, depth: 2, x: '40%', y: '50%' },
                          { name: 'Result Parse', itemStyle: { color: '#8b5cf6' }, depth: 2, x: '40%', y: '72%' },
                          { name: 'Error Handler', itemStyle: { color: '#8b5cf6' }, depth: 2, x: '40%', y: '92%' },

                          // Stage 3: Evaluation
                          { name: 'Judge Review', itemStyle: { color: '#fbbf24' }, depth: 3, x: '60%', y: '25%' },
                          { name: 'Score Calc', itemStyle: { color: '#fbbf24' }, depth: 3, x: '60%', y: '50%' },
                          { name: 'Telemetry Log', itemStyle: { color: '#fbbf24' }, depth: 3, x: '60%', y: '75%' },

                          // Stage 4: Final outcomes
                          { name: 'Pass', itemStyle: { color: '#10b981' }, depth: 4, x: '80%', y: '20%' },
                          { name: 'Partial', itemStyle: { color: '#fbbf24' }, depth: 4, x: '80%', y: '50%' },
                          { name: 'Fail', itemStyle: { color: '#ff4757' }, depth: 4, x: '80%', y: '80%' },

                          // Stage 5: Completion
                          { name: 'Eval Completed', itemStyle: { color: '#f37021' }, depth: 5, x: '95%', y: '50%' }
                        ],
                        links: [
                          // Stage 0 → 1
                          { source: 'Eval Started', target: 'Query Parser', value: 42, lineStyle: { color: '#f37021', opacity: 0.5 } },
                          { source: 'Eval Started', target: 'Tool Selector', value: 35, lineStyle: { color: '#f37021', opacity: 0.5 } },
                          { source: 'Eval Started', target: 'Context Builder', value: 23, lineStyle: { color: '#f37021', opacity: 0.5 } },

                          // Stage 1 → 2
                          { source: 'Query Parser', target: 'SQL Generation', value: 25, lineStyle: { color: '#60a5fa', opacity: 0.4 } },
                          { source: 'Query Parser', target: 'LLM Call', value: 17, lineStyle: { color: '#60a5fa', opacity: 0.4 } },
                          { source: 'Tool Selector', target: 'Tool Execution', value: 28, lineStyle: { color: '#60a5fa', opacity: 0.4 } },
                          { source: 'Tool Selector', target: 'Error Handler', value: 7, lineStyle: { color: '#60a5fa', opacity: 0.4 } },
                          { source: 'Context Builder', target: 'Result Parse', value: 18, lineStyle: { color: '#60a5fa', opacity: 0.4 } },
                          { source: 'Context Builder', target: 'Error Handler', value: 5, lineStyle: { color: '#60a5fa', opacity: 0.4 } },

                          // Stage 2 → 3
                          { source: 'SQL Generation', target: 'Judge Review', value: 22, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'SQL Generation', target: 'Telemetry Log', value: 3, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'LLM Call', target: 'Judge Review', value: 15, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'LLM Call', target: 'Telemetry Log', value: 2, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'Tool Execution', target: 'Score Calc', value: 24, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'Tool Execution', target: 'Telemetry Log', value: 4, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'Result Parse', target: 'Score Calc', value: 16, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'Result Parse', target: 'Telemetry Log', value: 2, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },
                          { source: 'Error Handler', target: 'Telemetry Log', value: 12, lineStyle: { color: '#8b5cf6', opacity: 0.4 } },

                          // Stage 3 → 4
                          { source: 'Judge Review', target: 'Pass', value: 28, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Judge Review', target: 'Partial', value: 7, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Judge Review', target: 'Fail', value: 2, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Score Calc', target: 'Pass', value: 30, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Score Calc', target: 'Partial', value: 8, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Score Calc', target: 'Fail', value: 2, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Telemetry Log', target: 'Pass', value: 15, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Telemetry Log', target: 'Partial', value: 5, lineStyle: { color: '#fbbf24', opacity: 0.4 } },
                          { source: 'Telemetry Log', target: 'Fail', value: 3, lineStyle: { color: '#fbbf24', opacity: 0.4 } },

                          // Stage 4 → 5
                          { source: 'Pass', target: 'Eval Completed', value: 73, lineStyle: { color: '#10b981', opacity: 0.5 } },
                          { source: 'Partial', target: 'Eval Completed', value: 20, lineStyle: { color: '#fbbf24', opacity: 0.5 } },
                          { source: 'Fail', target: 'Eval Completed', value: 7, lineStyle: { color: '#ff4757', opacity: 0.5 } }
                        ]
                      }
                    ]
                  }}
                  style={{ height: '600px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-3 gap-4 mt-6">
                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>PASS</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-green-500">73</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Successful evals</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#fbbf24', boxShadow: '0 0 8px rgba(251, 191, 36, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#fbbf24'}}>PARTIAL</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#fbbf24'}}>20</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Partial results</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#ff4757', boxShadow: '0 0 8px rgba(255, 71, 87, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-red-500">FAIL</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-red-500">7</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Failed executions</div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  VISUALIZATION FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">Multi-stage pipeline</span> - 5 execution stages from start to completion</li>
                  <li>• <span className="text-primary font-semibold">Color-coded flows</span> - Semantic colors for different processing stages</li>
                  <li>• <span className="text-primary font-semibold">No loops</span> - Directed acyclic graph (DAG) structure for ECharts compatibility</li>
                  <li>• <span className="text-primary font-semibold">Interactive tooltips</span> - Hover for detailed flow information</li>
                  <li>• <span className="text-primary font-semibold">Terminal aesthetic</span> - IBM Plex Mono labels with glass morphism</li>
                  <li>• <span className="text-primary font-semibold">Gradient flows</span> - Smooth color transitions between stages</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Time Series Graph */}
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Time Series Performance Graph</CardTitle>
            <CardDescription>Multi-metric eval performance tracking with trend analysis</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                EVAL PERFORMANCE METRICS - 24 HOUR WINDOW
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 2000,
                    animationEasing: 'cubicOut',
                    animationDelay: (idx: number) => idx * 100,
                    grid: {
                      left: '5%',
                      right: '5%',
                      bottom: '12%',
                      top: '15%',
                      containLabel: true
                    },
                    tooltip: {
                      trigger: 'axis',
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      axisPointer: {
                        type: 'cross',
                        crossStyle: {
                          color: '#f37021',
                          opacity: 0.5
                        },
                        lineStyle: {
                          color: '#f37021',
                          opacity: 0.3
                        }
                      }
                    },
                    legend: {
                      data: ['Success Rate', 'Response Time', 'Token Usage', 'Hallucination Score'],
                      textStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 11
                      },
                      top: '2%',
                      itemGap: 20,
                      icon: 'circle'
                    },
                    xAxis: {
                      type: 'category',
                      boundaryGap: false,
                      data: ['00:00', '02:00', '04:00', '06:00', '08:00', '10:00', '12:00', '14:00', '16:00', '18:00', '20:00', '22:00'],
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      splitLine: {
                        show: true,
                        lineStyle: {
                          color: '#ffffff0d',
                          type: 'dashed'
                        }
                      }
                    },
                    yAxis: [
                      {
                        type: 'value',
                        name: 'Percentage / Score',
                        position: 'left',
                        max: 100,
                        axisLine: {
                          lineStyle: {
                            color: '#ffffff1a'
                          }
                        },
                        axisLabel: {
                          color: '#b5b5b5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 10,
                          formatter: '{value}%'
                        },
                        splitLine: {
                          lineStyle: {
                            color: '#ffffff0d',
                            type: 'dashed'
                          }
                        }
                      },
                      {
                        type: 'value',
                        name: 'Tokens (K)',
                        position: 'right',
                        max: 10,
                        axisLine: {
                          lineStyle: {
                            color: '#ffffff1a'
                          }
                        },
                        axisLabel: {
                          color: '#b5b5b5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 10,
                          formatter: '{value}K'
                        },
                        splitLine: {
                          show: false
                        }
                      }
                    ],
                    series: [
                      {
                        name: 'Success Rate',
                        type: 'line',
                        data: [85, 87, 90, 88, 92, 91, 94, 93, 95, 94, 96, 95],
                        smooth: true,
                        symbol: 'circle',
                        symbolSize: 6,
                        lineStyle: {
                          color: '#10b981',
                          width: 3,
                          shadowBlur: 10,
                          shadowColor: 'rgba(16, 185, 129, 0.4)'
                        },
                        itemStyle: {
                          color: '#10b981',
                          borderColor: '#1a1a1a',
                          borderWidth: 2
                        },
                        areaStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: 'rgba(16, 185, 129, 0.3)' },
                              { offset: 1, color: 'rgba(16, 185, 129, 0.05)' }
                            ]
                          }
                        },
                        emphasis: {
                          focus: 'series',
                          lineStyle: {
                            width: 4
                          }
                        }
                      },
                      {
                        name: 'Response Time',
                        type: 'line',
                        data: [45, 48, 42, 50, 46, 44, 40, 38, 36, 39, 35, 37],
                        smooth: true,
                        symbol: 'circle',
                        symbolSize: 6,
                        lineStyle: {
                          color: '#f37021',
                          width: 3,
                          shadowBlur: 10,
                          shadowColor: 'rgba(243, 112, 33, 0.4)'
                        },
                        itemStyle: {
                          color: '#f37021',
                          borderColor: '#1a1a1a',
                          borderWidth: 2
                        },
                        areaStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: 'rgba(243, 112, 33, 0.3)' },
                              { offset: 1, color: 'rgba(243, 112, 33, 0.05)' }
                            ]
                          }
                        },
                        emphasis: {
                          focus: 'series',
                          lineStyle: {
                            width: 4
                          }
                        }
                      },
                      {
                        name: 'Token Usage',
                        type: 'line',
                        yAxisIndex: 1,
                        data: [6.2, 6.5, 5.8, 7.1, 6.8, 6.3, 5.9, 5.5, 5.2, 5.8, 5.1, 5.4],
                        smooth: true,
                        symbol: 'circle',
                        symbolSize: 6,
                        lineStyle: {
                          color: '#60a5fa',
                          width: 3,
                          shadowBlur: 10,
                          shadowColor: 'rgba(96, 165, 250, 0.4)'
                        },
                        itemStyle: {
                          color: '#60a5fa',
                          borderColor: '#1a1a1a',
                          borderWidth: 2
                        },
                        areaStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: 'rgba(96, 165, 250, 0.3)' },
                              { offset: 1, color: 'rgba(96, 165, 250, 0.05)' }
                            ]
                          }
                        },
                        emphasis: {
                          focus: 'series',
                          lineStyle: {
                            width: 4
                          }
                        }
                      },
                      {
                        name: 'Hallucination Score',
                        type: 'line',
                        data: [12, 10, 8, 11, 7, 6, 5, 6, 4, 5, 3, 4],
                        smooth: true,
                        symbol: 'circle',
                        symbolSize: 6,
                        lineStyle: {
                          color: '#ff4757',
                          width: 3,
                          shadowBlur: 10,
                          shadowColor: 'rgba(255, 71, 87, 0.4)'
                        },
                        itemStyle: {
                          color: '#ff4757',
                          borderColor: '#1a1a1a',
                          borderWidth: 2
                        },
                        areaStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: 'rgba(255, 71, 87, 0.3)' },
                              { offset: 1, color: 'rgba(255, 71, 87, 0.05)' }
                            ]
                          }
                        },
                        emphasis: {
                          focus: 'series',
                          lineStyle: {
                            width: 4
                          }
                        }
                      }
                    ]
                  }}
                  style={{ height: '500px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-4 gap-3 mt-6">
                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <TrendingUp className="h-4 w-4" style={{color: '#10b981'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>SUCCESS RATE</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-green-500">95%</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1 flex items-center space-x-1">
                    <span>+11% from start</span>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <Activity className="h-4 w-4 text-primary" />
                    <span className="text-xs font-mono font-semibold text-primary">RESPONSE TIME</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-primary">37%</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1 flex items-center space-x-1">
                    <span>-8% improvement</span>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#60a5fa', boxShadow: '0 0 8px rgba(96, 165, 250, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#60a5fa'}}>TOKEN USAGE</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#60a5fa'}}>5.4K</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1 flex items-center space-x-1">
                    <span>-0.8K optimized</span>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <AlertCircle className="h-4 w-4 text-red-500" />
                    <span className="text-xs font-mono font-semibold text-red-500">HALLUCINATION</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-red-500">4%</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1 flex items-center space-x-1">
                    <span>-8% reduction</span>
                  </div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  GRAPH FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">Multi-series time series</span> - 4 metrics tracked simultaneously over 24 hours</li>
                  <li>• <span className="text-primary font-semibold">Dual Y-axes</span> - Left for percentages, right for token counts</li>
                  <li>• <span className="text-primary font-semibold">Smooth curves</span> - Bezier interpolation for professional appearance</li>
                  <li>• <span className="text-primary font-semibold">Area fills</span> - Gradient fills with transparency for depth</li>
                  <li>• <span className="text-primary font-semibold">Glowing lines</span> - Subtle shadow effects matching terminal aesthetic</li>
                  <li>• <span className="text-primary font-semibold">Cross-hair tooltip</span> - Interactive axis pointer with orange accent</li>
                  <li>• <span className="text-primary font-semibold">Trend analysis</span> - Summary cards show deltas from start of period</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Donut/Pie Chart */}
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Multi-Ring Donut Chart</CardTitle>
            <CardDescription>Hierarchical eval result distribution with nested breakdown</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                EVAL RESULTS DISTRIBUTION - NESTED ANALYSIS
              </div>

              <div className="grid md:grid-cols-2 gap-6">
                {/* Main Donut Chart */}
                <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                  <ReactECharts
                    option={{
                      backgroundColor: 'transparent',
                      tooltip: {
                        trigger: 'item',
                        backgroundColor: 'rgba(26, 26, 26, 0.95)',
                        borderColor: '#f37021',
                        borderWidth: 1,
                        textStyle: {
                          color: '#f5f5f5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 12
                        },
                        formatter: (params: any) => {
                          return `<div style="padding: 4px;">
                            <div style="color: #f37021; font-weight: bold; margin-bottom: 4px;">${params.name}</div>
                            <div style="color: #b5b5b5;">Count: ${params.value} evals</div>
                            <div style="color: #b5b5b5;">Percentage: ${params.percent}%</div>
                          </div>`
                        }
                      },
                      legend: {
                        show: false
                      },
                      series: [
                        // Outer ring - Detailed breakdown
                        {
                          name: 'Detailed Results',
                          type: 'pie',
                          radius: ['60%', '85%'],
                          center: ['50%', '50%'],
                          avoidLabelOverlap: true,
                          itemStyle: {
                            borderColor: '#1a1a1a',
                            borderWidth: 3,
                            shadowBlur: 12,
                            shadowOffsetX: 0,
                            shadowOffsetY: 0
                          },
                          label: {
                            show: true,
                            position: 'outside',
                            color: '#b5b5b5',
                            fontFamily: 'IBM Plex Mono, monospace',
                            fontSize: 10,
                            fontWeight: 600,
                            formatter: '{b}\n{c}',
                            lineHeight: 14
                          },
                          labelLine: {
                            show: true,
                            length: 15,
                            length2: 10,
                            lineStyle: {
                              color: '#ffffff1a'
                            }
                          },
                          emphasis: {
                            itemStyle: {
                              shadowBlur: 20,
                              shadowOffsetX: 0,
                              shadowColor: 'rgba(243, 112, 33, 0.5)'
                            },
                            label: {
                              show: true,
                              fontSize: 12,
                              fontWeight: 'bold',
                              color: '#f37021'
                            }
                          },
                          data: [
                            {
                              value: 342,
                              name: 'Perfect Match',
                              itemStyle: {
                                color: '#10b981',
                                shadowColor: 'rgba(16, 185, 129, 0.4)'
                              }
                            },
                            {
                              value: 198,
                              name: 'Good Result',
                              itemStyle: {
                                color: '#34d399',
                                shadowColor: 'rgba(52, 211, 153, 0.4)'
                              }
                            },
                            {
                              value: 87,
                              name: 'Acceptable',
                              itemStyle: {
                                color: '#6ee7b7',
                                shadowColor: 'rgba(110, 231, 183, 0.4)'
                              }
                            },
                            {
                              value: 64,
                              name: 'Minor Issues',
                              itemStyle: {
                                color: '#fbbf24',
                                shadowColor: 'rgba(251, 191, 36, 0.4)'
                              }
                            },
                            {
                              value: 43,
                              name: 'Quality Warning',
                              itemStyle: {
                                color: '#fb923c',
                                shadowColor: 'rgba(251, 146, 60, 0.4)'
                              }
                            },
                            {
                              value: 28,
                              name: 'Failed Query',
                              itemStyle: {
                                color: '#ff4757',
                                shadowColor: 'rgba(255, 71, 87, 0.4)'
                              }
                            },
                            {
                              value: 15,
                              name: 'Critical Error',
                              itemStyle: {
                                color: '#dc2626',
                                shadowColor: 'rgba(220, 38, 38, 0.4)'
                              }
                            },
                            {
                              value: 23,
                              name: 'Timeout',
                              itemStyle: {
                                color: '#8b5cf6',
                                shadowColor: 'rgba(139, 92, 246, 0.4)'
                              }
                            }
                          ]
                        },
                        // Inner ring - Summary
                        {
                          name: 'Summary',
                          type: 'pie',
                          radius: ['0%', '50%'],
                          center: ['50%', '50%'],
                          itemStyle: {
                            borderColor: '#1a1a1a',
                            borderWidth: 3,
                            shadowBlur: 15,
                            shadowOffsetX: 0,
                            shadowOffsetY: 0
                          },
                          label: {
                            show: false
                          },
                          emphasis: {
                            itemStyle: {
                              shadowBlur: 25,
                              shadowOffsetX: 0
                            },
                            label: {
                              show: true,
                              fontSize: 18,
                              fontWeight: 'bold',
                              fontFamily: 'IBM Plex Mono, monospace',
                              color: '#f37021',
                              formatter: '{b}\n{d}%'
                            }
                          },
                          data: [
                            {
                              value: 627,
                              name: 'PASS',
                              itemStyle: {
                                color: '#10b981',
                                shadowColor: 'rgba(16, 185, 129, 0.6)'
                              }
                            },
                            {
                              value: 107,
                              name: 'PARTIAL',
                              itemStyle: {
                                color: '#fbbf24',
                                shadowColor: 'rgba(251, 191, 36, 0.6)'
                              }
                            },
                            {
                              value: 66,
                              name: 'FAIL',
                              itemStyle: {
                                color: '#ff4757',
                                shadowColor: 'rgba(255, 71, 87, 0.6)'
                              }
                            }
                          ]
                        }
                      ],
                      graphic: {
                        type: 'text',
                        left: 'center',
                        top: 'center',
                        style: {
                          text: '800\nTOTAL',
                          textAlign: 'center',
                          fill: '#f37021',
                          fontSize: 24,
                          fontWeight: 'bold',
                          fontFamily: 'IBM Plex Mono, monospace',
                          lineHeight: 30
                        }
                      }
                    }}
                    style={{ height: '450px', width: '100%' }}
                    opts={{ renderer: 'svg' }}
                  />
                </div>

                {/* Statistics Panel */}
                <div className="space-y-3">
                  <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                    BREAKDOWN STATISTICS
                  </div>

                  {/* Success Tier */}
                  <div className="glass border border-border/20 rounded-lg p-4">
                    <div className="flex items-center justify-between mb-3">
                      <div className="flex items-center space-x-2">
                        <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                        <span className="text-sm font-mono font-semibold" style={{color: '#10b981'}}>SUCCESS TIER</span>
                      </div>
                      <span className="text-xl font-bold metric-value text-green-500">78.4%</span>
                    </div>
                    <div className="space-y-2">
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Perfect Match</span>
                        <span className="font-mono" style={{color: '#10b981'}}>342 (42.8%)</span>
                      </div>
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Good Result</span>
                        <span className="font-mono" style={{color: '#34d399'}}>198 (24.8%)</span>
                      </div>
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Acceptable</span>
                        <span className="font-mono" style={{color: '#6ee7b7'}}>87 (10.9%)</span>
                      </div>
                    </div>
                  </div>

                  {/* Warning Tier */}
                  <div className="glass border border-border/20 rounded-lg p-4">
                    <div className="flex items-center justify-between mb-3">
                      <div className="flex items-center space-x-2">
                        <div className="w-3 h-3 rounded-full" style={{background: '#fbbf24', boxShadow: '0 0 8px rgba(251, 191, 36, 0.6)'}} />
                        <span className="text-sm font-mono font-semibold" style={{color: '#fbbf24'}}>WARNING TIER</span>
                      </div>
                      <span className="text-xl font-bold metric-value" style={{color: '#fbbf24'}}>13.4%</span>
                    </div>
                    <div className="space-y-2">
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Minor Issues</span>
                        <span className="font-mono" style={{color: '#fbbf24'}}>64 (8.0%)</span>
                      </div>
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Quality Warning</span>
                        <span className="font-mono" style={{color: '#fb923c'}}>43 (5.4%)</span>
                      </div>
                    </div>
                  </div>

                  {/* Error Tier */}
                  <div className="glass border border-border/20 rounded-lg p-4">
                    <div className="flex items-center justify-between mb-3">
                      <div className="flex items-center space-x-2">
                        <div className="w-3 h-3 rounded-full" style={{background: '#ff4757', boxShadow: '0 0 8px rgba(255, 71, 87, 0.6)'}} />
                        <span className="text-sm font-mono font-semibold text-red-500">ERROR TIER</span>
                      </div>
                      <span className="text-xl font-bold metric-value text-red-500">8.3%</span>
                    </div>
                    <div className="space-y-2">
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Failed Query</span>
                        <span className="font-mono text-red-500">28 (3.5%)</span>
                      </div>
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Critical Error</span>
                        <span className="font-mono" style={{color: '#dc2626'}}>15 (1.9%)</span>
                      </div>
                      <div className="flex justify-between text-xs">
                        <span className="text-muted-foreground font-sans">Timeout</span>
                        <span className="font-mono" style={{color: '#8b5cf6'}}>23 (2.9%)</span>
                      </div>
                    </div>
                  </div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  CHART FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">Multi-ring hierarchy</span> - Inner ring shows summary (Pass/Partial/Fail), outer ring shows detailed breakdown</li>
                  <li>• <span className="text-primary font-semibold">8-category taxonomy</span> - Granular classification from Perfect Match to Critical Error</li>
                  <li>• <span className="text-primary font-semibold">Color gradients</span> - Success tier uses green shades, warnings use orange, errors use red spectrum</li>
                  <li>• <span className="text-primary font-semibold">Center metric</span> - Total count displayed in center with orange accent</li>
                  <li>• <span className="text-primary font-semibold">Glowing slices</span> - Each segment has color-matched shadow blur effect</li>
                  <li>• <span className="text-primary font-semibold">Interactive emphasis</span> - Hover to highlight and see percentage with orange tooltip</li>
                  <li>• <span className="text-primary font-semibold">Statistics panel</span> - Tier-based breakdown with counts and percentages</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Bar Chart */}
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Grouped Bar Chart</CardTitle>
            <CardDescription>Model performance comparison across multiple evaluation metrics</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                MODEL BENCHMARK COMPARISON - 5 METRICS
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 1500,
                    animationEasing: 'elasticOut',
                    animationDelay: (idx: number) => idx * 80,
                    grid: {
                      left: '12%',
                      right: '5%',
                      bottom: '12%',
                      top: '12%',
                      containLabel: false
                    },
                    tooltip: {
                      trigger: 'axis',
                      axisPointer: {
                        type: 'shadow',
                        shadowStyle: {
                          color: 'rgba(243, 112, 33, 0.1)'
                        }
                      },
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      }
                    },
                    legend: {
                      data: ['Sonnet 4.5', 'Opus 4', 'GPT-4o', 'Gemini 2.0', 'Llama 3.3', 'Mistral Large', 'Cohere Command', 'Claude 3.5'],
                      textStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      top: '2%',
                      itemGap: 12,
                      type: 'scroll',
                      pageIconColor: '#f37021',
                      pageIconInactiveColor: '#2a2a2a',
                      pageTextStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace'
                      }
                    },
                    xAxis: {
                      type: 'category',
                      data: ['Accuracy', 'Speed', 'Cost Efficiency', 'SQL Quality', 'Error Handling'],
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10,
                        rotate: 0,
                        interval: 0
                      },
                      axisTick: {
                        show: false
                      }
                    },
                    yAxis: {
                      type: 'value',
                      max: 100,
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      splitLine: {
                        lineStyle: {
                          color: '#ffffff0d',
                          type: 'dashed'
                        }
                      }
                    },
                    series: [
                      {
                        name: 'Sonnet 4.5',
                        type: 'bar',
                        data: [95, 88, 92, 96, 94],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#f37021' },
                              { offset: 1, color: '#d96219' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(243, 112, 33, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(243, 112, 33, 0.6)'
                          }
                        }
                      },
                      {
                        name: 'Opus 4',
                        type: 'bar',
                        data: [92, 85, 88, 94, 91],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#10b981' },
                              { offset: 1, color: '#059669' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(16, 185, 129, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(16, 185, 129, 0.6)'
                          }
                        }
                      },
                      {
                        name: 'GPT-4o',
                        type: 'bar',
                        data: [89, 91, 85, 90, 88],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#60a5fa' },
                              { offset: 1, color: '#3b82f6' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(96, 165, 250, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(96, 165, 250, 0.6)'
                          }
                        }
                      },
                      {
                        name: 'Gemini 2.0',
                        type: 'bar',
                        data: [87, 93, 90, 85, 89],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#8b5cf6' },
                              { offset: 1, color: '#7c3aed' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(139, 92, 246, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(139, 92, 246, 0.6)'
                          }
                        }
                      },
                      {
                        name: 'Claude 3.5',
                        type: 'bar',
                        data: [91, 87, 89, 93, 90],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#fbbf24' },
                              { offset: 1, color: '#f59e0b' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(251, 191, 36, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(251, 191, 36, 0.6)'
                          }
                        }
                      },
                      {
                        name: 'Llama 3.3',
                        type: 'bar',
                        data: [82, 86, 91, 80, 84],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#ec4899' },
                              { offset: 1, color: '#db2777' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(236, 72, 153, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(236, 72, 153, 0.6)'
                          }
                        }
                      },
                      {
                        name: 'Mistral Large',
                        type: 'bar',
                        data: [86, 84, 88, 87, 86],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#14b8a6' },
                              { offset: 1, color: '#0d9488' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(20, 184, 166, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(20, 184, 166, 0.6)'
                          }
                        }
                      },
                      {
                        name: 'Cohere Command',
                        type: 'bar',
                        data: [80, 82, 85, 79, 81],
                        itemStyle: {
                          color: {
                            type: 'linear',
                            x: 0,
                            y: 0,
                            x2: 0,
                            y2: 1,
                            colorStops: [
                              { offset: 0, color: '#f97316' },
                              { offset: 1, color: '#ea580c' }
                            ]
                          },
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowColor: 'rgba(249, 115, 22, 0.4)',
                          shadowOffsetY: 4
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(249, 115, 22, 0.6)'
                          }
                        }
                      }
                    ]
                  }}
                  style={{ height: '450px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-4 gap-3 mt-6">
                <div className="glass border border-primary/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full bg-primary" style={{boxShadow: '0 0 8px rgba(243, 112, 33, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-primary">SONNET 4.5</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-primary">93.0</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Anthropic</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#fbbf24', boxShadow: '0 0 8px rgba(251, 191, 36, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#fbbf24'}}>CLAUDE 3.5</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#fbbf24'}}>90.0</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Anthropic</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>OPUS 4</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-green-500">90.0</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Anthropic</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#8b5cf6', boxShadow: '0 0 8px rgba(139, 92, 246, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#8b5cf6'}}>GEMINI 2.0</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#8b5cf6'}}>88.8</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Google</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#60a5fa', boxShadow: '0 0 8px rgba(96, 165, 250, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#60a5fa'}}>GPT-4O</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#60a5fa'}}>88.6</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">OpenAI</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#14b8a6', boxShadow: '0 0 8px rgba(20, 184, 166, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#14b8a6'}}>MISTRAL LARGE</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#14b8a6'}}>86.2</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Mistral AI</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#ec4899', boxShadow: '0 0 8px rgba(236, 72, 153, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#ec4899'}}>LLAMA 3.3</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#ec4899'}}>84.6</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Meta</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#f97316', boxShadow: '0 0 8px rgba(249, 115, 22, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#f97316'}}>COHERE CMD</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#f97316'}}>81.4</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Cohere</div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  CHART FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">Grouped bars</span> - 5 models × 5 metrics = 25 data points visualized</li>
                  <li>• <span className="text-primary font-semibold">Gradient fills</span> - Vertical gradients from bright to darker shades</li>
                  <li>• <span className="text-primary font-semibold">Glowing shadows</span> - Color-matched shadow blur on each bar</li>
                  <li>• <span className="text-primary font-semibold">Rounded tops</span> - 4px border radius for modern aesthetic</li>
                  <li>• <span className="text-primary font-semibold">Interactive emphasis</span> - Enhanced glow on hover with orange accent</li>
                  <li>• <span className="text-primary font-semibold">Model comparison</span> - Clear visual ranking across evaluation dimensions</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Heatmap */}
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Performance Heatmap</CardTitle>
            <CardDescription>Eval success rate intensity by day and hour with temporal patterns</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                WEEKLY PERFORMANCE MATRIX - HOURLY BREAKDOWN
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    grid: {
                      left: '10%',
                      right: '5%',
                      bottom: '20%',
                      top: '5%',
                      containLabel: false
                    },
                    tooltip: {
                      position: 'top',
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      formatter: (params: any) => {
                        const [hour, day] = params.value;
                        const success = params.data[2];
                        const dayNames = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
                        return `<div style="padding: 4px;">
                          <div style="color: #f37021; font-weight: bold;">${dayNames[day]} ${hour}:00</div>
                          <div style="color: #b5b5b5; margin-top: 4px;">Success Rate: ${success}%</div>
                          <div style="color: #b5b5b5;">Status: ${success >= 85 ? 'Excellent' : success >= 70 ? 'Good' : success >= 50 ? 'Fair' : 'Poor'}</div>
                        </div>`
                      }
                    },
                    xAxis: {
                      type: 'category',
                      data: ['0h', '2h', '4h', '6h', '8h', '10h', '12h', '14h', '16h', '18h', '20h', '22h'],
                      splitArea: {
                        show: false
                      },
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      }
                    },
                    yAxis: {
                      type: 'category',
                      data: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'],
                      splitArea: {
                        show: false
                      },
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      }
                    },
                    visualMap: {
                      min: 40,
                      max: 100,
                      calculable: true,
                      orient: 'horizontal',
                      left: 'center',
                      bottom: '2%',
                      inverse: true,
                      textStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      inRange: {
                        color: ['#ff4757', '#fb923c', '#fbbf24', '#34d399', '#10b981']
                      }
                    },
                    series: [
                      {
                        name: 'Success Rate',
                        type: 'heatmap',
                        data: [
                          // Sunday
                          [0, 0, 68], [1, 0, 72], [2, 0, 75], [3, 0, 78], [4, 0, 82], [5, 0, 88],
                          [6, 0, 92], [7, 0, 95], [8, 0, 93], [9, 0, 89], [10, 0, 85], [11, 0, 80],
                          // Monday
                          [0, 1, 75], [1, 1, 80], [2, 1, 85], [3, 1, 90], [4, 1, 94], [5, 1, 96],
                          [6, 1, 98], [7, 1, 97], [8, 1, 95], [9, 1, 92], [10, 1, 88], [11, 1, 83],
                          // Tuesday
                          [0, 2, 73], [1, 2, 78], [2, 2, 83], [3, 2, 88], [4, 2, 92], [5, 2, 95],
                          [6, 2, 97], [7, 2, 96], [8, 2, 94], [9, 2, 90], [10, 2, 86], [11, 2, 81],
                          // Wednesday
                          [0, 3, 76], [1, 3, 81], [2, 3, 86], [3, 3, 91], [4, 3, 95], [5, 3, 97],
                          [6, 3, 99], [7, 3, 98], [8, 3, 96], [9, 3, 93], [10, 3, 89], [11, 3, 84],
                          // Thursday
                          [0, 4, 74], [1, 4, 79], [2, 4, 84], [3, 4, 89], [4, 4, 93], [5, 4, 96],
                          [6, 4, 98], [7, 4, 97], [8, 4, 95], [9, 4, 91], [10, 4, 87], [11, 4, 82],
                          // Friday
                          [0, 5, 70], [1, 5, 75], [2, 5, 80], [3, 5, 85], [4, 5, 89], [5, 5, 93],
                          [6, 5, 95], [7, 5, 94], [8, 5, 92], [9, 5, 88], [10, 5, 84], [11, 5, 79],
                          // Saturday
                          [0, 6, 65], [1, 6, 70], [2, 6, 74], [3, 6, 79], [4, 6, 83], [5, 6, 87],
                          [6, 6, 90], [7, 6, 92], [8, 6, 90], [9, 6, 86], [10, 6, 82], [11, 6, 77]
                        ],
                        label: {
                          show: true,
                          color: '#f5f5f5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 9,
                          fontWeight: 600,
                          formatter: (params: any) => params.data[2]
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(243, 112, 33, 0.6)',
                            borderColor: '#f37021',
                            borderWidth: 2
                          },
                          label: {
                            fontSize: 11,
                            color: '#f37021'
                          }
                        },
                        itemStyle: {
                          borderColor: '#1a1a1a',
                          borderWidth: 2
                        }
                      }
                    ]
                  }}
                  style={{ height: '400px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-3 gap-4 mt-6">
                <div className="glass border border-border/20 rounded-lg p-4">
                  <div className="flex items-center space-x-2 mb-3">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>PEAK HOURS</span>
                  </div>
                  <div className="text-sm text-muted-foreground font-sans space-y-1">
                    <div>Wed 12h-14h: <span className="font-mono" style={{color: '#10b981'}}>99%</span></div>
                    <div>Mon 12h-14h: <span className="font-mono" style={{color: '#10b981'}}>98%</span></div>
                    <div>Thu 12h-14h: <span className="font-mono" style={{color: '#10b981'}}>98%</span></div>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-4">
                  <div className="flex items-center space-x-2 mb-3">
                    <div className="w-3 h-3 rounded-full" style={{background: '#fbbf24', boxShadow: '0 0 8px rgba(251, 191, 36, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#fbbf24'}}>AVG WEEKDAY</span>
                  </div>
                  <div className="text-3xl font-bold metric-value" style={{color: '#fbbf24'}}>89%</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-2">Mon-Fri Average</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-4">
                  <div className="flex items-center space-x-2 mb-3">
                    <div className="w-3 h-3 rounded-full" style={{background: '#ff4757', boxShadow: '0 0 8px rgba(255, 71, 87, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-red-500">LOW HOURS</span>
                  </div>
                  <div className="text-sm text-muted-foreground font-sans space-y-1">
                    <div>Sat 0h-2h: <span className="font-mono text-red-500">65%</span></div>
                    <div>Sun 0h-2h: <span className="font-mono text-red-500">68%</span></div>
                    <div>Fri 22h-24h: <span className="font-mono text-red-500">70%</span></div>
                  </div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  HEATMAP FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">7×12 matrix</span> - 84 cells showing hourly performance across full week</li>
                  <li>• <span className="text-primary font-semibold">5-color gradient</span> - Red → Orange → Yellow → Light Green → Dark Green (40%-100%)</li>
                  <li>• <span className="text-primary font-semibold">Cell labels</span> - Percentage displayed in each cell with IBM Plex Mono</li>
                  <li>• <span className="text-primary font-semibold">Temporal patterns</span> - Clear visualization of weekday peaks vs weekend dips</li>
                  <li>• <span className="text-primary font-semibold">Interactive emphasis</span> - Orange border and enhanced glow on hover</li>
                  <li>• <span className="text-primary font-semibold">Visual scale</span> - Horizontal legend shows color mapping to success rates</li>
                  <li>• <span className="text-primary font-semibold">Pattern analysis</span> - Peak hours, averages, and low periods highlighted in stats</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Scatter Plot */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Scatter Plot</CardTitle>
            <CardDescription>Cost vs Quality correlation analysis for model selection</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                MODEL COST-QUALITY TRADE-OFF ANALYSIS
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 1500,
                    animationEasing: 'cubicOut',
                    grid: {
                      left: '12%',
                      right: '5%',
                      bottom: '12%',
                      top: '12%',
                      containLabel: false
                    },
                    tooltip: {
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      formatter: (params: any) => {
                        return `<div style="padding: 4px;">
                          <div style="color: #f37021; font-weight: bold; margin-bottom: 4px;">${params.seriesName}</div>
                          <div style="color: #b5b5b5;">Cost: $${params.value[0]}/1K tokens</div>
                          <div style="color: #b5b5b5;">Quality Score: ${params.value[1]}</div>
                        </div>`
                      }
                    },
                    xAxis: {
                      type: 'value',
                      name: 'Cost per 1K Tokens ($)',
                      nameLocation: 'middle',
                      nameGap: 30,
                      nameTextStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 11
                      },
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10,
                        formatter: '${value}'
                      },
                      splitLine: {
                        lineStyle: {
                          color: '#ffffff0d',
                          type: 'dashed'
                        }
                      }
                    },
                    yAxis: {
                      type: 'value',
                      name: 'Quality Score',
                      nameLocation: 'middle',
                      nameGap: 40,
                      nameTextStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 11
                      },
                      max: 100,
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      splitLine: {
                        lineStyle: {
                          color: '#ffffff0d',
                          type: 'dashed'
                        }
                      }
                    },
                    series: [
                      {
                        name: 'LLM Models',
                        type: 'scatter',
                        data: [
                          // Anthropic
                          { value: [0.015, 93], name: 'Sonnet 4.5', itemStyle: { color: '#f37021', shadowBlur: 15, shadowColor: 'rgba(243, 112, 33, 0.6)' }, symbolSize: 22 },
                          { value: [0.012, 90], name: 'Claude 3.5', itemStyle: { color: '#fbbf24', shadowBlur: 15, shadowColor: 'rgba(251, 191, 36, 0.6)' }, symbolSize: 20 },
                          { value: [0.025, 90], name: 'Opus 4', itemStyle: { color: '#10b981', shadowBlur: 15, shadowColor: 'rgba(16, 185, 129, 0.6)' }, symbolSize: 20 },
                          { value: [0.010, 88], name: 'Claude 3 Opus', itemStyle: { color: '#059669', shadowBlur: 12, shadowColor: 'rgba(5, 150, 105, 0.5)' }, symbolSize: 17 },
                          { value: [0.003, 85], name: 'Claude 3 Haiku', itemStyle: { color: '#84cc16', shadowBlur: 12, shadowColor: 'rgba(132, 204, 22, 0.5)' }, symbolSize: 16 },

                          // OpenAI
                          { value: [0.008, 88.6], name: 'GPT-4o', itemStyle: { color: '#60a5fa', shadowBlur: 15, shadowColor: 'rgba(96, 165, 250, 0.6)' }, symbolSize: 19 },
                          { value: [0.010, 87.5], name: 'GPT-4 Turbo', itemStyle: { color: '#3b82f6', shadowBlur: 12, shadowColor: 'rgba(59, 130, 246, 0.5)' }, symbolSize: 18 },
                          { value: [0.030, 86], name: 'GPT-4', itemStyle: { color: '#2563eb', shadowBlur: 12, shadowColor: 'rgba(37, 99, 235, 0.5)' }, symbolSize: 17 },
                          { value: [0.0005, 78], name: 'GPT-3.5 Turbo', itemStyle: { color: '#1e40af', shadowBlur: 10, shadowColor: 'rgba(30, 64, 175, 0.4)' }, symbolSize: 14 },
                          { value: [0.0002, 72], name: 'GPT-3.5', itemStyle: { color: '#1e3a8a', shadowBlur: 10, shadowColor: 'rgba(30, 58, 138, 0.4)' }, symbolSize: 12 },

                          // Google
                          { value: [0.005, 88.8], name: 'Gemini 2.0', itemStyle: { color: '#8b5cf6', shadowBlur: 15, shadowColor: 'rgba(139, 92, 246, 0.6)' }, symbolSize: 19 },
                          { value: [0.004, 86.5], name: 'Gemini 1.5 Pro', itemStyle: { color: '#7c3aed', shadowBlur: 12, shadowColor: 'rgba(124, 58, 237, 0.5)' }, symbolSize: 17 },
                          { value: [0.001, 82], name: 'Gemini 1.5 Flash', itemStyle: { color: '#6d28d9', shadowBlur: 10, shadowColor: 'rgba(109, 40, 217, 0.4)' }, symbolSize: 15 },
                          { value: [0.0003, 75], name: 'Gemini 1.0 Pro', itemStyle: { color: '#5b21b6', shadowBlur: 10, shadowColor: 'rgba(91, 33, 182, 0.4)' }, symbolSize: 13 },

                          // Meta
                          { value: [0.001, 84.6], name: 'Llama 3.3 70B', itemStyle: { color: '#ec4899', shadowBlur: 15, shadowColor: 'rgba(236, 72, 153, 0.6)' }, symbolSize: 17 },
                          { value: [0.0005, 81], name: 'Llama 3.2 90B', itemStyle: { color: '#db2777', shadowBlur: 12, shadowColor: 'rgba(219, 39, 119, 0.5)' }, symbolSize: 15 },
                          { value: [0.0002, 76], name: 'Llama 3.1 70B', itemStyle: { color: '#be185d', shadowBlur: 10, shadowColor: 'rgba(190, 24, 93, 0.4)' }, symbolSize: 13 },
                          { value: [0.0001, 72], name: 'Llama 3 70B', itemStyle: { color: '#9f1239', shadowBlur: 10, shadowColor: 'rgba(159, 18, 57, 0.4)' }, symbolSize: 12 },
                          { value: [0.00005, 65], name: 'Llama 2 70B', itemStyle: { color: '#881337', shadowBlur: 8, shadowColor: 'rgba(136, 19, 55, 0.3)' }, symbolSize: 10 },

                          // Mistral
                          { value: [0.004, 86.2], name: 'Mistral Large 2', itemStyle: { color: '#14b8a6', shadowBlur: 15, shadowColor: 'rgba(20, 184, 166, 0.6)' }, symbolSize: 17 },
                          { value: [0.002, 83], name: 'Mistral Medium', itemStyle: { color: '#0d9488', shadowBlur: 12, shadowColor: 'rgba(13, 148, 136, 0.5)' }, symbolSize: 15 },
                          { value: [0.0007, 78], name: 'Mistral Small', itemStyle: { color: '#0f766e', shadowBlur: 10, shadowColor: 'rgba(15, 118, 110, 0.4)' }, symbolSize: 13 },
                          { value: [0.0003, 71], name: 'Mistral 7B', itemStyle: { color: '#115e59', shadowBlur: 8, shadowColor: 'rgba(17, 94, 89, 0.3)' }, symbolSize: 11 },

                          // Cohere
                          { value: [0.003, 81.4], name: 'Command R+', itemStyle: { color: '#f97316', shadowBlur: 15, shadowColor: 'rgba(249, 115, 22, 0.6)' }, symbolSize: 16 },
                          { value: [0.001, 77], name: 'Command R', itemStyle: { color: '#ea580c', shadowBlur: 12, shadowColor: 'rgba(234, 88, 12, 0.5)' }, symbolSize: 14 },
                          { value: [0.0005, 73], name: 'Command', itemStyle: { color: '#c2410c', shadowBlur: 10, shadowColor: 'rgba(194, 65, 12, 0.4)' }, symbolSize: 12 },
                          { value: [0.0002, 68], name: 'Command Light', itemStyle: { color: '#9a3412', shadowBlur: 8, shadowColor: 'rgba(154, 52, 18, 0.3)' }, symbolSize: 10 },

                          // Anthropic extended
                          { value: [0.0024, 82], name: 'Claude 2.1', itemStyle: { color: '#a3e635', shadowBlur: 10, shadowColor: 'rgba(163, 230, 53, 0.4)' }, symbolSize: 14 },
                          { value: [0.0008, 78], name: 'Claude 2', itemStyle: { color: '#65a30d', shadowBlur: 8, shadowColor: 'rgba(101, 163, 13, 0.3)' }, symbolSize: 12 },
                          { value: [0.0004, 73], name: 'Claude Instant', itemStyle: { color: '#4d7c0f', shadowBlur: 8, shadowColor: 'rgba(77, 124, 15, 0.3)' }, symbolSize: 10 },

                          // Others
                          { value: [0.006, 84], name: 'Perplexity Sonar Large', itemStyle: { color: '#06b6d4', shadowBlur: 12, shadowColor: 'rgba(6, 182, 212, 0.5)' }, symbolSize: 16 },
                          { value: [0.002, 79], name: 'Perplexity Sonar', itemStyle: { color: '#0891b2', shadowBlur: 10, shadowColor: 'rgba(8, 145, 178, 0.4)' }, symbolSize: 13 },
                          { value: [0.0018, 80], name: 'Databricks DBRX', itemStyle: { color: '#f59e0b', shadowBlur: 10, shadowColor: 'rgba(245, 158, 11, 0.4)' }, symbolSize: 14 },
                          { value: [0.0012, 76], name: 'Yi Large', itemStyle: { color: '#eab308', shadowBlur: 10, shadowColor: 'rgba(234, 179, 8, 0.4)' }, symbolSize: 13 },
                          { value: [0.0004, 70], name: 'Yi 34B', itemStyle: { color: '#ca8a04', shadowBlur: 8, shadowColor: 'rgba(202, 138, 4, 0.3)' }, symbolSize: 11 },
                          { value: [0.0025, 83], name: 'Inflection-2.5', itemStyle: { color: '#a855f7', shadowBlur: 10, shadowColor: 'rgba(168, 85, 247, 0.4)' }, symbolSize: 15 },
                          { value: [0.0015, 79], name: 'DeepSeek V2', itemStyle: { color: '#9333ea', shadowBlur: 10, shadowColor: 'rgba(147, 51, 234, 0.4)' }, symbolSize: 14 }
                        ],
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 25,
                            shadowColor: 'rgba(243, 112, 33, 0.8)'
                          }
                        }
                      }
                    ]
                  }}
                  style={{ height: '500px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  SCATTER PLOT FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">37 LLM models plotted</span> - Comprehensive comparison across Anthropic, OpenAI, Google, Meta, Mistral, Cohere, and others</li>
                  <li>• <span className="text-primary font-semibold">Cost-quality correlation</span> - X-axis: cost per 1K tokens, Y-axis: quality score (0-100)</li>
                  <li>• <span className="text-primary font-semibold">Bubble sizing</span> - Larger bubbles for higher-performing models, smaller for budget options</li>
                  <li>• <span className="text-primary font-semibold">Color-coded by provider</span> - Each model family has distinct color palette for easy identification</li>
                  <li>• <span className="text-primary font-semibold">Glowing points</span> - Shadow blur effect for depth and visual separation in dense regions</li>
                  <li>• <span className="text-primary font-semibold">Sweet spot identification</span> - Top-right quadrant shows best value models (high quality, reasonable cost)</li>
                  <li>• <span className="text-primary font-semibold">Interactive tooltips</span> - Hover for exact cost and quality metrics per model</li>
                  <li>• <span className="text-primary font-semibold">Model evolution visible</span> - Compare versions (GPT-3.5 vs GPT-4, Llama 2 vs Llama 3.3, Claude 2 vs Sonnet 4.5)</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Radar Chart */}
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Radar Chart</CardTitle>
            <CardDescription>7 major LLM providers compared across 8 dimensions - realistic trade-offs visible</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                8-DIMENSIONAL MODEL CAPABILITY ANALYSIS
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 2000,
                    animationEasing: 'elasticOut',
                    tooltip: {
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      }
                    },
                    legend: {
                      data: ['Sonnet 4.5', 'Opus 4', 'GPT-4o', 'Gemini 2.0', 'Llama 3.3', 'Mistral Large', 'Command R+'],
                      textStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      top: '2%',
                      type: 'scroll'
                    },
                    radar: {
                      indicator: [
                        { name: 'SQL Quality', max: 100 },
                        { name: 'Accuracy', max: 100 },
                        { name: 'Speed', max: 100 },
                        { name: 'Cost Efficiency', max: 100 },
                        { name: 'Error Handling', max: 100 },
                        { name: 'Context Understanding', max: 100 },
                        { name: 'Reasoning', max: 100 },
                        { name: 'Tool Usage', max: 100 }
                      ],
                      shape: 'polygon',
                      splitNumber: 5,
                      axisName: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 11,
                        fontWeight: 600
                      },
                      splitLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      splitArea: {
                        show: true,
                        areaStyle: {
                          color: ['rgba(243, 112, 33, 0.02)', 'rgba(243, 112, 33, 0.04)']
                        }
                      },
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      }
                    },
                    series: [
                      {
                        name: 'Model Capabilities',
                        type: 'radar',
                        data: [
                          {
                            value: [94, 93, 82, 75, 92, 96, 95, 91],  // SQL, Acc, Speed, Cost, Error, Context, Reason, Tool
                            name: 'Sonnet 4.5',
                            itemStyle: { color: '#f37021' },
                            lineStyle: {
                              color: '#f37021',
                              width: 2.5,
                              shadowBlur: 12,
                              shadowColor: 'rgba(243, 112, 33, 0.4)'
                            },
                            areaStyle: { color: 'rgba(243, 112, 33, 0.12)' },
                            symbol: 'circle',
                            symbolSize: 6
                          },
                          {
                            value: [96, 95, 68, 60, 94, 97, 97, 89],  // Opus: highest quality, slowest, most expensive
                            name: 'Opus 4',
                            itemStyle: { color: '#10b981' },
                            lineStyle: {
                              color: '#10b981',
                              width: 2.5,
                              shadowBlur: 12,
                              shadowColor: 'rgba(16, 185, 129, 0.4)'
                            },
                            areaStyle: { color: 'rgba(16, 185, 129, 0.12)' },
                            symbol: 'circle',
                            symbolSize: 6
                          },
                          {
                            value: [88, 87, 90, 82, 86, 89, 89, 85],  // GPT-4o: balanced, good speed
                            name: 'GPT-4o',
                            itemStyle: { color: '#60a5fa' },
                            lineStyle: {
                              color: '#60a5fa',
                              width: 2.5,
                              shadowBlur: 12,
                              shadowColor: 'rgba(96, 165, 250, 0.4)'
                            },
                            areaStyle: { color: 'rgba(96, 165, 250, 0.12)' },
                            symbol: 'circle',
                            symbolSize: 6
                          },
                          {
                            value: [82, 84, 94, 88, 85, 86, 87, 92],  // Gemini: fast, great multimodal/tools
                            name: 'Gemini 2.0',
                            itemStyle: { color: '#8b5cf6' },
                            lineStyle: {
                              color: '#8b5cf6',
                              width: 2.5,
                              shadowBlur: 12,
                              shadowColor: 'rgba(139, 92, 246, 0.4)'
                            },
                            areaStyle: { color: 'rgba(139, 92, 246, 0.12)' },
                            symbol: 'circle',
                            symbolSize: 6
                          },
                          {
                            value: [76, 79, 88, 96, 80, 78, 81, 79],  // Llama: great cost efficiency, decent quality
                            name: 'Llama 3.3',
                            itemStyle: { color: '#ec4899' },
                            lineStyle: {
                              color: '#ec4899',
                              width: 2.5,
                              shadowBlur: 12,
                              shadowColor: 'rgba(236, 72, 153, 0.4)'
                            },
                            areaStyle: { color: 'rgba(236, 72, 153, 0.12)' },
                            symbol: 'circle',
                            symbolSize: 6
                          },
                          {
                            value: [85, 84, 84, 85, 84, 83, 86, 83],  // Mistral: solid all-around
                            name: 'Mistral Large',
                            itemStyle: { color: '#14b8a6' },
                            lineStyle: {
                              color: '#14b8a6',
                              width: 2.5,
                              shadowBlur: 12,
                              shadowColor: 'rgba(20, 184, 166, 0.4)'
                            },
                            areaStyle: { color: 'rgba(20, 184, 166, 0.12)' },
                            symbol: 'circle',
                            symbolSize: 6
                          },
                          {
                            value: [78, 80, 85, 90, 78, 76, 79, 87],  // Cohere: good retrieval/RAG, cost efficient
                            name: 'Command R+',
                            itemStyle: { color: '#f97316' },
                            lineStyle: {
                              color: '#f97316',
                              width: 2.5,
                              shadowBlur: 12,
                              shadowColor: 'rgba(249, 115, 22, 0.4)'
                            },
                            areaStyle: { color: 'rgba(249, 115, 22, 0.12)' },
                            symbol: 'circle',
                            symbolSize: 6
                          }
                        ]
                      }
                    ]
                  }}
                  style={{ height: '550px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-2 gap-3 mt-6">
                <div className="glass border border-primary/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full bg-primary" style={{boxShadow: '0 0 8px rgba(243, 112, 33, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-primary">SONNET 4.5</span>
                  </div>
                  <div className="text-xs text-muted-foreground font-sans space-y-1">
                    <div>Context (96), Reasoning (95), SQL (94)</div>
                    <div className="text-[10px] text-muted-foreground/60 mt-1">Best overall quality, slower speed</div>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>OPUS 4</span>
                  </div>
                  <div className="text-xs text-muted-foreground font-sans space-y-1">
                    <div>Context (97), Reasoning (97), SQL (96)</div>
                    <div className="text-[10px] text-muted-foreground/60 mt-1">Highest quality, expensive & slow</div>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#60a5fa', boxShadow: '0 0 8px rgba(96, 165, 250, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#60a5fa'}}>GPT-4O</span>
                  </div>
                  <div className="text-xs text-muted-foreground font-sans space-y-1">
                    <div>Speed (90), Reasoning (89), Context (89)</div>
                    <div className="text-[10px] text-muted-foreground/60 mt-1">Balanced performance across all areas</div>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#8b5cf6', boxShadow: '0 0 8px rgba(139, 92, 246, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#8b5cf6'}}>GEMINI 2.0</span>
                  </div>
                  <div className="text-xs text-muted-foreground font-sans space-y-1">
                    <div>Speed (94), Tool Usage (92), Cost (88)</div>
                    <div className="text-[10px] text-muted-foreground/60 mt-1">Fastest with great multimodal</div>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#ec4899', boxShadow: '0 0 8px rgba(236, 72, 153, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#ec4899'}}>LLAMA 3.3</span>
                  </div>
                  <div className="text-xs text-muted-foreground font-sans space-y-1">
                    <div>Cost Efficiency (96), Speed (88), Acc (79)</div>
                    <div className="text-[10px] text-muted-foreground/60 mt-1">Best value for cost-sensitive tasks</div>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#14b8a6', boxShadow: '0 0 8px rgba(20, 184, 166, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#14b8a6'}}>MISTRAL LARGE</span>
                  </div>
                  <div className="text-xs text-muted-foreground font-sans space-y-1">
                    <div>Reasoning (86), SQL (85), Cost (85)</div>
                    <div className="text-[10px] text-muted-foreground/60 mt-1">Solid all-around performer</div>
                  </div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  RADAR CHART FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">7 providers compared</span> - Sonnet 4.5, Opus 4, GPT-4o, Gemini 2.0, Llama 3.3, Mistral Large, Command R+ across 8 dimensions</li>
                  <li>• <span className="text-primary font-semibold">8-dimensional analysis</span> - SQL quality, accuracy, speed, cost efficiency, error handling, context understanding, reasoning, tool usage</li>
                  <li>• <span className="text-primary font-semibold">Realistic trade-offs visible</span> - Opus 4 leads quality but scores low on speed/cost; Llama excels at cost but lower accuracy</li>
                  <li>• <span className="text-primary font-semibold">Overlapping polygons</span> - Semi-transparent areas show where models overlap and where they diverge</li>
                  <li>• <span className="text-primary font-semibold">Polygon shape</span> - 8-sided radar with 5-level gradations (0-20-40-60-80-100)</li>
                  <li>• <span className="text-primary font-semibold">Glowing lines</span> - Color-matched shadow blur for depth and model identification</li>
                  <li>• <span className="text-primary font-semibold">Sweet spot identification</span> - Quickly identify which model excels in specific dimensions for your use case</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Box Plot */}
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Box Plot</CardTitle>
            <CardDescription>Response time distribution analysis showing variability and outliers</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                MODEL RESPONSE TIME DISTRIBUTION - MILLISECONDS
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 1800,
                    animationEasing: 'cubicOut',
                    grid: {
                      left: '15%',
                      right: '5%',
                      bottom: '12%',
                      top: '8%',
                      containLabel: false
                    },
                    tooltip: {
                      trigger: 'item',
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      formatter: (params: any) => {
                        const data = params.data;
                        return `<div style="padding: 6px;">
                          <div style="color: #f37021; font-weight: bold; margin-bottom: 6px;">${params.name}</div>
                          <div style="color: #b5b5b5;">Max: ${data[5]}ms</div>
                          <div style="color: #b5b5b5;">Q3 (75%): ${data[4]}ms</div>
                          <div style="color: #b5b5b5;">Median: ${data[3]}ms</div>
                          <div style="color: #b5b5b5;">Q1 (25%): ${data[2]}ms</div>
                          <div style="color: #b5b5b5;">Min: ${data[1]}ms</div>
                        </div>`
                      }
                    },
                    xAxis: {
                      type: 'category',
                      data: [
                        'Sonnet 4.5', 'Claude 3.5', 'Opus 4', 'Claude 3 Opus', 'Claude 3 Haiku', 'Claude 2.1', 'Claude 2', 'Claude Instant',
                        'GPT-4o', 'GPT-4 Turbo', 'GPT-4', 'GPT-3.5 Turbo', 'GPT-3.5',
                        'Gemini 2.0', 'Gemini 1.5 Pro', 'Gemini 1.5 Flash', 'Gemini 1.0',
                        'Llama 3.3', 'Llama 3.2', 'Llama 3.1', 'Llama 3',
                        'Mistral Large', 'Mistral Medium', 'Command R+'
                      ],
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 9,
                        rotate: 35,
                        interval: 0
                      },
                      axisTick: {
                        show: false
                      }
                    },
                    yAxis: {
                      type: 'value',
                      name: 'Response Time (ms)',
                      nameLocation: 'middle',
                      nameGap: 50,
                      nameTextStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 11
                      },
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      splitLine: {
                        lineStyle: {
                          color: '#ffffff0d',
                          type: 'dashed'
                        }
                      }
                    },
                    series: [
                      {
                        name: 'Response Time',
                        type: 'boxplot',
                        barWidth: '25%',
                        data: [
                          // Format: [min, Q1, median, Q3, max]
                          // Anthropic family
                          [1200, 1800, 2100, 2500, 3200],   // Sonnet 4.5
                          [1100, 1600, 1900, 2300, 2900],   // Claude 3.5
                          [1500, 2200, 2600, 3100, 4000],   // Opus 4
                          [1400, 2000, 2400, 2900, 3800],   // Claude 3 Opus
                          [600, 1100, 1350, 1700, 2300],    // Claude 3 Haiku
                          [1300, 1900, 2300, 2800, 3700],   // Claude 2.1
                          [1400, 2100, 2500, 3000, 3900],   // Claude 2
                          [700, 1200, 1500, 1900, 2600],    // Claude Instant

                          // OpenAI family
                          [900, 1400, 1700, 2100, 2800],    // GPT-4o
                          [1000, 1500, 1850, 2250, 3000],   // GPT-4 Turbo
                          [1600, 2300, 2700, 3200, 4100],   // GPT-4
                          [400, 800, 1000, 1300, 1800],     // GPT-3.5 Turbo
                          [500, 900, 1150, 1500, 2100],     // GPT-3.5

                          // Google family
                          [800, 1300, 1600, 2000, 2700],    // Gemini 2.0
                          [850, 1350, 1700, 2100, 2850],    // Gemini 1.5 Pro
                          [500, 950, 1200, 1550, 2200],     // Gemini 1.5 Flash
                          [900, 1450, 1800, 2250, 3100],    // Gemini 1.0

                          // Meta family
                          [700, 1100, 1400, 1800, 2400],    // Llama 3.3
                          [750, 1200, 1500, 1900, 2550],    // Llama 3.2
                          [800, 1300, 1650, 2050, 2800],    // Llama 3.1
                          [850, 1400, 1750, 2200, 3000],    // Llama 3

                          // Others
                          [1000, 1500, 1800, 2200, 2900],   // Mistral Large
                          [950, 1450, 1750, 2150, 2850],    // Mistral Medium
                          [950, 1450, 1750, 2150, 2850]     // Command R+
                        ],
                        itemStyle: {
                          color: '#f37021',
                          borderColor: '#f37021',
                          borderWidth: 2,
                          shadowBlur: 10,
                          shadowColor: 'rgba(243, 112, 33, 0.4)'
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(243, 112, 33, 0.6)',
                            borderWidth: 3
                          }
                        }
                      }
                    ]
                  }}
                  style={{ height: '600px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-4 gap-3 mt-6">
                <div className="glass border border-primary/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>FASTEST MEDIAN</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#10b981'}}>1.0s</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">GPT-3.5 Turbo</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#60a5fa', boxShadow: '0 0 8px rgba(96, 165, 250, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#60a5fa'}}>MOST CONSISTENT</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#60a5fa'}}>±0.5s</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">GPT-3.5 Turbo IQR</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#fbbf24', boxShadow: '0 0 8px rgba(251, 191, 36, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#fbbf24'}}>HIGHEST VARIANCE</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#fbbf24'}}>±1.5s</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">GPT-4 / Opus 4 IQR</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full bg-primary" style={{boxShadow: '0 0 8px rgba(243, 112, 33, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-primary">AVG MEDIAN</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-primary">1.8s</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Across 24 models</div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  BOX PLOT FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">5-number summary</span> - Minimum, Q1 (25th percentile), Median, Q3 (75th percentile), Maximum for each model</li>
                  <li>• <span className="text-primary font-semibold">IQR visualization</span> - Interquartile range (box) shows middle 50% of data</li>
                  <li>• <span className="text-primary font-semibold">Outlier detection</span> - Whiskers extend to min/max, showing full range including outliers</li>
                  <li>• <span className="text-primary font-semibold">Consistency comparison</span> - Narrower boxes indicate more consistent performance</li>
                  <li>• <span className="text-primary font-semibold">Median comparison</span> - Center line in each box shows typical response time</li>
                  <li>• <span className="text-primary font-semibold">Orange glow theme</span> - Teradata orange with shadow blur on hover</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Waterfall Chart */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Waterfall Chart</CardTitle>
            <CardDescription>Eval execution pipeline breakdown showing cumulative time and cost progression</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                EVAL EXECUTION PIPELINE - TIME BREAKDOWN
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 1800,
                    animationEasing: 'cubicOut',
                    animationDelay: (idx: number) => idx * 150,
                    grid: {
                      left: '12%',
                      right: '5%',
                      bottom: '15%',
                      top: '8%',
                      containLabel: false
                    },
                    tooltip: {
                      trigger: 'axis',
                      axisPointer: {
                        type: 'shadow'
                      },
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      formatter: (params: any) => {
                        const data = params[0];
                        const stageName = data.name;
                        const duration = data.value;
                        return `<div style="padding: 6px;">
                          <div style="color: #f37021; font-weight: bold; margin-bottom: 4px;">${stageName}</div>
                          <div style="color: #b5b5b5;">Duration: ${duration}ms</div>
                          <div style="color: #b5b5b5;">Cost: $${(duration * 0.00001).toFixed(5)}</div>
                        </div>`
                      }
                    },
                    legend: {
                      data: ['Baseline', 'Increment', 'Total'],
                      textStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      top: '2%'
                    },
                    xAxis: {
                      type: 'category',
                      data: ['Start', 'Query Parse', 'LLM Call', 'Tool Execute', 'Result Parse', 'Judge Review', 'Complete'],
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10,
                        rotate: 25,
                        interval: 0
                      },
                      axisTick: {
                        show: false
                      }
                    },
                    yAxis: {
                      type: 'value',
                      name: 'Cumulative Time (ms)',
                      nameLocation: 'middle',
                      nameGap: 50,
                      nameTextStyle: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 11
                      },
                      axisLine: {
                        lineStyle: {
                          color: '#ffffff1a'
                        }
                      },
                      axisLabel: {
                        color: '#b5b5b5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 10
                      },
                      splitLine: {
                        lineStyle: {
                          color: '#ffffff0d',
                          type: 'dashed'
                        }
                      }
                    },
                    series: [
                      // Invisible baseline to stack the increments on
                      {
                        name: 'Baseline',
                        type: 'bar',
                        stack: 'total',
                        itemStyle: {
                          borderColor: 'transparent',
                          color: 'transparent'
                        },
                        emphasis: {
                          itemStyle: {
                            borderColor: 'transparent',
                            color: 'transparent'
                          }
                        },
                        data: [0, 0, 200, 1700, 2500, 2800, 3400]
                      },
                      // Visible increments (the waterfall bars)
                      {
                        name: 'Increment',
                        type: 'bar',
                        stack: 'total',
                        label: {
                          show: true,
                          position: 'inside',
                          formatter: (params: any) => {
                            const value = params.value;
                            return value > 0 ? `+${value}ms` : '';
                          },
                          color: '#ffffff',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 11,
                          fontWeight: 600
                        },
                        itemStyle: {
                          borderRadius: [4, 4, 0, 0],
                          shadowBlur: 10,
                          shadowOffsetY: 4
                        },
                        data: [
                          {
                            value: 0,
                            itemStyle: {
                              color: '#ffffff1a',
                              shadowColor: 'rgba(255, 255, 255, 0.1)'
                            }
                          },
                          {
                            value: 200,
                            itemStyle: {
                              color: '#60a5fa',
                              shadowColor: 'rgba(96, 165, 250, 0.4)'
                            }
                          },
                          {
                            value: 1500,
                            itemStyle: {
                              color: '#f37021',
                              shadowColor: 'rgba(243, 112, 33, 0.5)'
                            }
                          },
                          {
                            value: 800,
                            itemStyle: {
                              color: '#8b5cf6',
                              shadowColor: 'rgba(139, 92, 246, 0.4)'
                            }
                          },
                          {
                            value: 300,
                            itemStyle: {
                              color: '#10b981',
                              shadowColor: 'rgba(16, 185, 129, 0.4)'
                            }
                          },
                          {
                            value: 600,
                            itemStyle: {
                              color: '#fbbf24',
                              shadowColor: 'rgba(251, 191, 36, 0.4)'
                            }
                          },
                          {
                            value: 0,
                            itemStyle: {
                              color: '#10b981',
                              shadowColor: 'rgba(16, 185, 129, 0.5)'
                            }
                          }
                        ],
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20
                          }
                        }
                      },
                      // Total line connecting the tops
                      {
                        name: 'Total',
                        type: 'line',
                        data: [0, 200, 1700, 2500, 2800, 3400, 3400],
                        lineStyle: {
                          color: '#f37021',
                          width: 3,
                          type: 'dashed',
                          shadowBlur: 10,
                          shadowColor: 'rgba(243, 112, 33, 0.4)'
                        },
                        itemStyle: {
                          color: '#f37021',
                          borderColor: '#1a1a1a',
                          borderWidth: 2
                        },
                        symbolSize: 8,
                        symbol: 'circle',
                        z: 100
                      }
                    ]
                  }}
                  style={{ height: '500px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-4 gap-3 mt-6">
                <div className="glass border border-primary/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full bg-primary" style={{boxShadow: '0 0 8px rgba(243, 112, 33, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-primary">LLM CALL</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-primary">1500ms</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">44% of total time</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#8b5cf6', boxShadow: '0 0 8px rgba(139, 92, 246, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#8b5cf6'}}>TOOL EXECUTE</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#8b5cf6'}}>800ms</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">24% of total time</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#fbbf24', boxShadow: '0 0 8px rgba(251, 191, 36, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#fbbf24'}}>JUDGE REVIEW</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#fbbf24'}}>600ms</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">18% of total time</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>TOTAL TIME</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-green-500">3.4s</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">End-to-end duration</div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  WATERFALL CHART FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">Cumulative time visualization</span> - Each bar shows incremental contribution stacked on previous stages</li>
                  <li>• <span className="text-primary font-semibold">Color-coded stages</span> - Blue (parsing), Orange (LLM), Purple (tool), Green (parsing), Yellow (judge)</li>
                  <li>• <span className="text-primary font-semibold">Floating bars</span> - Transparent baseline creates waterfall effect with bars starting at cumulative position</li>
                  <li>• <span className="text-primary font-semibold">Total line overlay</span> - Dashed orange line connects the top of each stage showing cumulative progression</li>
                  <li>• <span className="text-primary font-semibold">Stage duration labels</span> - Each bar shows "+Xms" increment inside the bar</li>
                  <li>• <span className="text-primary font-semibold">Cost estimation</span> - Tooltip shows estimated cost per stage ($0.00001 per ms)</li>
                  <li>• <span className="text-primary font-semibold">Pipeline optimization insights</span> - Easily identify bottlenecks (LLM call is 44% of total time)</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Parallel Coordinates */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Parallel Coordinates</CardTitle>
            <CardDescription>High-dimensional model comparison across 10 metrics for pattern discovery</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                10-DIMENSIONAL LLM MODEL ANALYSIS
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 2000,
                    animationEasing: 'cubicOut',
                    tooltip: {
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      }
                    },
                    parallelAxis: [
                      { dim: 0, name: 'SQL Quality', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 1, name: 'Accuracy', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 2, name: 'Speed', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 3, name: 'Cost Efficiency', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 4, name: 'Error Handling', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 5, name: 'Context', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 6, name: 'Reasoning', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 7, name: 'Tool Usage', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 8, name: 'Hallucination Resist', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } },
                      { dim: 9, name: 'Token Efficiency', max: 100, nameTextStyle: { color: '#b5b5b5', fontFamily: 'IBM Plex Mono, monospace', fontSize: 10 } }
                    ],
                    parallel: {
                      left: '8%',
                      right: '8%',
                      bottom: '15%',
                      top: '12%',
                      parallelAxisDefault: {
                        type: 'value',
                        nameLocation: 'end',
                        nameGap: 20,
                        splitNumber: 5,
                        axisLine: {
                          lineStyle: {
                            color: '#ffffff1a'
                          }
                        },
                        axisLabel: {
                          color: '#b5b5b5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 9
                        },
                        splitLine: {
                          lineStyle: {
                            color: '#ffffff0d'
                          }
                        }
                      }
                    },
                    series: [
                      {
                        name: 'LLM Models',
                        type: 'parallel',
                        lineStyle: {
                          width: 3,
                          opacity: 0.6
                        },
                        emphasis: {
                          lineStyle: {
                            width: 5,
                            opacity: 1
                          }
                        },
                        data: [
                          {
                            name: 'Sonnet 4.5',
                            value: [96, 95, 88, 92, 94, 97, 96, 93, 91, 89],
                            lineStyle: {
                              color: '#f37021',
                              shadowBlur: 12,
                              shadowColor: 'rgba(243, 112, 33, 0.5)'
                            }
                          },
                          {
                            name: 'Claude 3.5',
                            value: [93, 90, 87, 89, 90, 91, 92, 87, 88, 86],
                            lineStyle: {
                              color: '#fbbf24',
                              shadowBlur: 12,
                              shadowColor: 'rgba(251, 191, 36, 0.4)'
                            }
                          },
                          {
                            name: 'Opus 4',
                            value: [94, 92, 85, 88, 91, 93, 95, 90, 90, 84],
                            lineStyle: {
                              color: '#10b981',
                              shadowBlur: 12,
                              shadowColor: 'rgba(16, 185, 129, 0.4)'
                            }
                          },
                          {
                            name: 'GPT-4o',
                            value: [90, 89, 91, 85, 88, 91, 92, 87, 85, 88],
                            lineStyle: {
                              color: '#60a5fa',
                              shadowBlur: 12,
                              shadowColor: 'rgba(96, 165, 250, 0.4)'
                            }
                          },
                          {
                            name: 'Gemini 2.0',
                            value: [85, 87, 93, 90, 89, 88, 90, 92, 84, 91],
                            lineStyle: {
                              color: '#8b5cf6',
                              shadowBlur: 12,
                              shadowColor: 'rgba(139, 92, 246, 0.4)'
                            }
                          },
                          {
                            name: 'Mistral Large',
                            value: [87, 86, 84, 88, 86, 85, 88, 85, 83, 87],
                            lineStyle: {
                              color: '#14b8a6',
                              shadowBlur: 12,
                              shadowColor: 'rgba(20, 184, 166, 0.4)'
                            }
                          },
                          {
                            name: 'Llama 3.3',
                            value: [80, 82, 86, 91, 84, 81, 85, 83, 80, 89],
                            lineStyle: {
                              color: '#ec4899',
                              shadowBlur: 12,
                              shadowColor: 'rgba(236, 72, 153, 0.4)'
                            }
                          },
                          {
                            name: 'Cohere Command',
                            value: [79, 80, 82, 85, 81, 78, 82, 80, 77, 84],
                            lineStyle: {
                              color: '#f97316',
                              shadowBlur: 12,
                              shadowColor: 'rgba(249, 115, 22, 0.4)'
                            }
                          }
                        ]
                      }
                    ]
                  }}
                  style={{ height: '550px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-2 gap-4 mt-6">
                <div className="glass border border-primary/20 rounded-lg p-4">
                  <div className="flex items-center space-x-2 mb-3">
                    <div className="w-3 h-3 rounded-full bg-primary" style={{boxShadow: '0 0 8px rgba(243, 112, 33, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-primary">PATTERN INSIGHTS</span>
                  </div>
                  <div className="text-sm text-muted-foreground font-sans space-y-2">
                    <div>• <span className="font-mono text-primary">Sonnet 4.5</span> leads in Context (97) and SQL Quality (96)</div>
                    <div>• <span className="font-mono" style={{color: '#8b5cf6'}}>Gemini 2.0</span> excels in Speed (93) and Tool Usage (92)</div>
                    <div>• <span className="font-mono" style={{color: '#ec4899'}}>Llama 3.3</span> best Cost Efficiency (91) and Token Efficiency (89)</div>
                    <div>• <span className="font-mono" style={{color: '#60a5fa'}}>GPT-4o</span> balanced across all dimensions</div>
                  </div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-4">
                  <div className="flex items-center space-x-2 mb-3">
                    <Activity className="h-4 w-4 text-primary" />
                    <span className="text-xs font-mono font-semibold text-primary">INTERACTIVE FILTERING</span>
                  </div>
                  <div className="text-sm text-muted-foreground font-sans space-y-2">
                    <div>• Click axis to brush/filter models by dimension range</div>
                    <div>• Hover over lines to highlight individual models</div>
                    <div>• Parallel lines indicate similar performance profiles</div>
                    <div>• Crossing lines reveal trade-offs between metrics</div>
                  </div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  PARALLEL COORDINATES FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">10-dimensional comparison</span> - Visualize all 8 models across 10 evaluation metrics simultaneously</li>
                  <li>• <span className="text-primary font-semibold">Polyline representation</span> - Each model is a continuous line threading through all dimension axes</li>
                  <li>• <span className="text-primary font-semibold">Pattern discovery</span> - Parallel lines indicate similar models, crossing lines show trade-offs</li>
                  <li>• <span className="text-primary font-semibold">Interactive brushing</span> - Click and drag on any axis to filter models by that dimension's range</li>
                  <li>• <span className="text-primary font-semibold">Hover emphasis</span> - Highlight individual models to trace their performance profile</li>
                  <li>• <span className="text-primary font-semibold">Glowing lines</span> - Color-coded with shadow blur for depth and model identification</li>
                  <li>• <span className="text-primary font-semibold">High-dimensional insights</span> - Identify clusters, outliers, and optimal models for specific use cases</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Tree Map */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Tree Map</CardTitle>
            <CardDescription>Hierarchical token consumption breakdown by model family and operation type</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                TOKEN CONSUMPTION HIERARCHY - PROPORTIONAL ANALYSIS
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 1500,
                    animationEasing: 'cubicOut',
                    tooltip: {
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      formatter: (params: any) => {
                        const data = params.data;
                        if (!data) return '';
                        const percent = ((data.value / 1850000) * 100).toFixed(1);
                        return `<div style="padding: 6px;">
                          <div style="color: #f37021; font-weight: bold; margin-bottom: 4px;">${data.name}</div>
                          <div style="color: #b5b5b5;">Tokens: ${data.value.toLocaleString()}</div>
                          <div style="color: #b5b5b5;">Percentage: ${percent}%</div>
                          <div style="color: #b5b5b5;">Cost: $${(data.value * 0.000015).toFixed(2)}</div>
                        </div>`
                      }
                    },
                    series: [
                      {
                        name: 'Token Consumption',
                        type: 'treemap',
                        width: '100%',
                        height: '100%',
                        roam: false,
                        nodeClick: false,
                        breadcrumb: {
                          show: false
                        },
                        label: {
                          show: true,
                          formatter: (params: any) => {
                            const data = params.data;
                            if (!data) return '';
                            if (data.value > 50000) {
                              return `{name|${data.name}}\n{value|${(data.value / 1000).toFixed(0)}K}`;
                            }
                            return '';
                          },
                          rich: {
                            name: {
                              color: '#f5f5f5',
                              fontFamily: 'IBM Plex Mono, monospace',
                              fontSize: 11,
                              fontWeight: 600
                            },
                            value: {
                              color: '#f5f5f5',
                              fontFamily: 'IBM Plex Mono, monospace',
                              fontSize: 13,
                              fontWeight: 'bold'
                            }
                          }
                        },
                        itemStyle: {
                          borderColor: '#1a1a1a',
                          borderWidth: 3,
                          gapWidth: 3,
                          shadowBlur: 10,
                          shadowColor: 'rgba(0, 0, 0, 0.3)'
                        },
                        emphasis: {
                          itemStyle: {
                            shadowBlur: 20,
                            shadowColor: 'rgba(243, 112, 33, 0.5)',
                            borderColor: '#f37021',
                            borderWidth: 3
                          },
                          label: {
                            show: true,
                            color: '#f37021'
                          }
                        },
                        levels: [
                          {
                            itemStyle: {
                              borderWidth: 0,
                              gapWidth: 5
                            }
                          },
                          {
                            itemStyle: {
                              gapWidth: 3
                            },
                            colorMappingBy: 'id',
                            color: [
                              '#f37021',  // Anthropic
                              '#60a5fa',  // OpenAI
                              '#8b5cf6',  // Google
                              '#10b981',  // Meta
                              '#14b8a6',  // Mistral
                              '#f97316'   // Cohere
                            ]
                          },
                          {
                            colorSaturation: [0.35, 0.5],
                            itemStyle: {
                              gapWidth: 1,
                              borderColorSaturation: 0.6
                            }
                          }
                        ],
                        data: [
                          {
                            name: 'Anthropic',
                            value: 725000,
                            itemStyle: { color: '#f37021' },
                            children: [
                              {
                                name: 'Sonnet 4.5',
                                value: 420000,
                                children: [
                                  { name: 'Input', value: 180000, itemStyle: { color: 'rgba(243, 112, 33, 0.9)' } },
                                  { name: 'Output', value: 160000, itemStyle: { color: 'rgba(243, 112, 33, 0.7)' } },
                                  { name: 'System', value: 80000, itemStyle: { color: 'rgba(243, 112, 33, 0.5)' } }
                                ]
                              },
                              {
                                name: 'Claude 3.5',
                                value: 205000,
                                children: [
                                  { name: 'Input', value: 85000, itemStyle: { color: 'rgba(251, 191, 36, 0.9)' } },
                                  { name: 'Output', value: 80000, itemStyle: { color: 'rgba(251, 191, 36, 0.7)' } },
                                  { name: 'System', value: 40000, itemStyle: { color: 'rgba(251, 191, 36, 0.5)' } }
                                ]
                              },
                              {
                                name: 'Opus 4',
                                value: 100000,
                                children: [
                                  { name: 'Input', value: 42000, itemStyle: { color: 'rgba(16, 185, 129, 0.9)' } },
                                  { name: 'Output', value: 38000, itemStyle: { color: 'rgba(16, 185, 129, 0.7)' } },
                                  { name: 'System', value: 20000, itemStyle: { color: 'rgba(16, 185, 129, 0.5)' } }
                                ]
                              }
                            ]
                          },
                          {
                            name: 'OpenAI',
                            value: 580000,
                            itemStyle: { color: '#60a5fa' },
                            children: [
                              {
                                name: 'GPT-4o',
                                value: 380000,
                                children: [
                                  { name: 'Input', value: 160000, itemStyle: { color: 'rgba(96, 165, 250, 0.9)' } },
                                  { name: 'Output', value: 145000, itemStyle: { color: 'rgba(96, 165, 250, 0.7)' } },
                                  { name: 'System', value: 75000, itemStyle: { color: 'rgba(96, 165, 250, 0.5)' } }
                                ]
                              },
                              {
                                name: 'GPT-4',
                                value: 200000,
                                children: [
                                  { name: 'Input', value: 85000, itemStyle: { color: 'rgba(59, 130, 246, 0.9)' } },
                                  { name: 'Output', value: 75000, itemStyle: { color: 'rgba(59, 130, 246, 0.7)' } },
                                  { name: 'System', value: 40000, itemStyle: { color: 'rgba(59, 130, 246, 0.5)' } }
                                ]
                              }
                            ]
                          },
                          {
                            name: 'Google',
                            value: 325000,
                            itemStyle: { color: '#8b5cf6' },
                            children: [
                              {
                                name: 'Gemini 2.0',
                                value: 220000,
                                children: [
                                  { name: 'Input', value: 95000, itemStyle: { color: 'rgba(139, 92, 246, 0.9)' } },
                                  { name: 'Output', value: 85000, itemStyle: { color: 'rgba(139, 92, 246, 0.7)' } },
                                  { name: 'System', value: 40000, itemStyle: { color: 'rgba(139, 92, 246, 0.5)' } }
                                ]
                              },
                              {
                                name: 'Gemini 1.5',
                                value: 105000,
                                children: [
                                  { name: 'Input', value: 45000, itemStyle: { color: 'rgba(124, 58, 237, 0.9)' } },
                                  { name: 'Output', value: 40000, itemStyle: { color: 'rgba(124, 58, 237, 0.7)' } },
                                  { name: 'System', value: 20000, itemStyle: { color: 'rgba(124, 58, 237, 0.5)' } }
                                ]
                              }
                            ]
                          },
                          {
                            name: 'Meta',
                            value: 120000,
                            itemStyle: { color: '#ec4899' },
                            children: [
                              {
                                name: 'Llama 3.3',
                                value: 120000,
                                children: [
                                  { name: 'Input', value: 52000, itemStyle: { color: 'rgba(236, 72, 153, 0.9)' } },
                                  { name: 'Output', value: 48000, itemStyle: { color: 'rgba(236, 72, 153, 0.7)' } },
                                  { name: 'System', value: 20000, itemStyle: { color: 'rgba(236, 72, 153, 0.5)' } }
                                ]
                              }
                            ]
                          },
                          {
                            name: 'Mistral',
                            value: 65000,
                            itemStyle: { color: '#14b8a6' },
                            children: [
                              {
                                name: 'Mistral Large',
                                value: 65000,
                                children: [
                                  { name: 'Input', value: 28000, itemStyle: { color: 'rgba(20, 184, 166, 0.9)' } },
                                  { name: 'Output', value: 25000, itemStyle: { color: 'rgba(20, 184, 166, 0.7)' } },
                                  { name: 'System', value: 12000, itemStyle: { color: 'rgba(20, 184, 166, 0.5)' } }
                                ]
                              }
                            ]
                          },
                          {
                            name: 'Cohere',
                            value: 35000,
                            itemStyle: { color: '#f97316' },
                            children: [
                              {
                                name: 'Cohere Command',
                                value: 35000,
                                children: [
                                  { name: 'Input', value: 15000, itemStyle: { color: 'rgba(249, 115, 22, 0.9)' } },
                                  { name: 'Output', value: 13000, itemStyle: { color: 'rgba(249, 115, 22, 0.7)' } },
                                  { name: 'System', value: 7000, itemStyle: { color: 'rgba(249, 115, 22, 0.5)' } }
                                ]
                              }
                            ]
                          }
                        ]
                      }
                    ]
                  }}
                  style={{ height: '600px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-4 gap-3 mt-6">
                <div className="glass border border-primary/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full bg-primary" style={{boxShadow: '0 0 8px rgba(243, 112, 33, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-primary">ANTHROPIC</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-primary">725K</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">39.2% of total tokens</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#60a5fa', boxShadow: '0 0 8px rgba(96, 165, 250, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#60a5fa'}}>OPENAI</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#60a5fa'}}>580K</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">31.4% of total tokens</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#8b5cf6', boxShadow: '0 0 8px rgba(139, 92, 246, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#8b5cf6'}}>GOOGLE</span>
                  </div>
                  <div className="text-2xl font-bold metric-value" style={{color: '#8b5cf6'}}>325K</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">17.6% of total tokens</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>TOTAL</span>
                  </div>
                  <div className="text-2xl font-bold metric-value text-green-500">1.85M</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">All providers</div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  TREE MAP FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">3-level hierarchy</span> - Provider family → Model → Operation type (Input/Output/System tokens)</li>
                  <li>• <span className="text-primary font-semibold">Proportional rectangles</span> - Area directly represents token consumption volume</li>
                  <li>• <span className="text-primary font-semibold">Color-coded families</span> - Each provider family has unique color (Anthropic=Orange, OpenAI=Blue, Google=Purple)</li>
                  <li>• <span className="text-primary font-semibold">Nested visualization</span> - Drill down from family to model to operation with visual hierarchy</li>
                  <li>• <span className="text-primary font-semibold">Cost estimation</span> - Tooltip shows token count and estimated cost at $0.000015 per token</li>
                  <li>• <span className="text-primary font-semibold">Quick comparison</span> - Instantly see which models/operations consume most resources</li>
                  <li>• <span className="text-primary font-semibold">Resource optimization</span> - Identify high-consumption areas for cost reduction opportunities</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Chord Diagram */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <Card className="glass border-border/50 mt-6">
          <CardHeader>
            <CardTitle className="font-mono">Chord Diagram</CardTitle>
            <CardDescription>Model comparison frequency matrix showing which models are evaluated together</CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-4">
                INTER-MODEL COMPARISON RELATIONSHIPS
              </div>

              <div className="glass border border-border/30 rounded-lg p-4" style={{background: 'rgba(13, 13, 13, 0.6)'}}>
                <ReactECharts
                  option={{
                    backgroundColor: 'transparent',
                    animation: true,
                    animationDuration: 2000,
                    animationEasing: 'elasticOut',
                    tooltip: {
                      backgroundColor: 'rgba(26, 26, 26, 0.95)',
                      borderColor: '#f37021',
                      borderWidth: 1,
                      textStyle: {
                        color: '#f5f5f5',
                        fontFamily: 'IBM Plex Mono, monospace',
                        fontSize: 12
                      },
                      formatter: (params: any) => {
                        if (params.dataType === 'edge') {
                          return `<div style="padding: 6px;">
                            <div style="color: #f37021; font-weight: bold; margin-bottom: 4px;">${params.data.source} ↔ ${params.data.target}</div>
                            <div style="color: #b5b5b5;">Comparisons: ${params.data.value}</div>
                            <div style="color: #b5b5b5;">Correlation: ${(params.data.value / 150 * 100).toFixed(0)}%</div>
                          </div>`
                        }
                        return '';
                      }
                    },
                    series: [
                      {
                        type: 'graph',
                        layout: 'circular',
                        circular: {
                          rotateLabel: true
                        },
                        data: [
                          { name: 'Sonnet 4.5', symbolSize: 60, itemStyle: { color: '#f37021', borderColor: '#1a1a1a', borderWidth: 2, shadowBlur: 20, shadowColor: '#f3702199' } },
                          { name: 'Opus 4', symbolSize: 55, itemStyle: { color: '#10b981', borderColor: '#1a1a1a', borderWidth: 2, shadowBlur: 18, shadowColor: '#10b98199' } },
                          { name: 'GPT-4o', symbolSize: 58, itemStyle: { color: '#60a5fa', borderColor: '#1a1a1a', borderWidth: 2, shadowBlur: 19, shadowColor: '#60a5fa99' } },
                          { name: 'Gemini 2.0', symbolSize: 54, itemStyle: { color: '#8b5cf6', borderColor: '#1a1a1a', borderWidth: 2, shadowBlur: 17, shadowColor: '#8b5cf699' } },
                          { name: 'Claude 3.5', symbolSize: 52, itemStyle: { color: '#fbbf24', borderColor: '#1a1a1a', borderWidth: 2, shadowBlur: 16, shadowColor: '#fbbf2499' } },
                          { name: 'Llama 3.3', symbolSize: 48, itemStyle: { color: '#ec4899', borderColor: '#1a1a1a', borderWidth: 2, shadowBlur: 14, shadowColor: '#ec489999' } },
                          { name: 'Mistral Large', symbolSize: 50, itemStyle: { color: '#14b8a6', borderColor: '#1a1a1a', borderWidth: 2, shadowBlur: 15, shadowColor: '#14b8a699' } }
                        ],
                        links: [
                          // Anthropic internal comparisons
                          { source: 'Sonnet 4.5', target: 'Opus 4', value: 145, lineStyle: { width: 4, opacity: 0.5 } },
                          { source: 'Sonnet 4.5', target: 'Claude 3.5', value: 128, lineStyle: { width: 3.5, opacity: 0.45 } },
                          { source: 'Opus 4', target: 'Claude 3.5', value: 98, lineStyle: { width: 2.5, opacity: 0.4 } },

                          // Anthropic vs OpenAI (most common comparison)
                          { source: 'Sonnet 4.5', target: 'GPT-4o', value: 167, lineStyle: { width: 5, opacity: 0.6 } },
                          { source: 'Opus 4', target: 'GPT-4o', value: 134, lineStyle: { width: 3.8, opacity: 0.5 } },
                          { source: 'Claude 3.5', target: 'GPT-4o', value: 112, lineStyle: { width: 3, opacity: 0.45 } },

                          // Anthropic vs Google
                          { source: 'Sonnet 4.5', target: 'Gemini 2.0', value: 143, lineStyle: { width: 4, opacity: 0.5 } },
                          { source: 'Opus 4', target: 'Gemini 2.0', value: 108, lineStyle: { width: 3, opacity: 0.45 } },
                          { source: 'Claude 3.5', target: 'Gemini 2.0', value: 95, lineStyle: { width: 2.5, opacity: 0.4 } },

                          // OpenAI vs Google
                          { source: 'GPT-4o', target: 'Gemini 2.0', value: 156, lineStyle: { width: 4.5, opacity: 0.55 } },

                          // Budget comparisons (Llama vs others)
                          { source: 'Llama 3.3', target: 'Sonnet 4.5', value: 89, lineStyle: { width: 2.2, opacity: 0.4 } },
                          { source: 'Llama 3.3', target: 'GPT-4o', value: 102, lineStyle: { width: 2.8, opacity: 0.42 } },
                          { source: 'Llama 3.3', target: 'Gemini 2.0', value: 118, lineStyle: { width: 3.2, opacity: 0.45 } },
                          { source: 'Llama 3.3', target: 'Mistral Large', value: 87, lineStyle: { width: 2.2, opacity: 0.4 } },

                          // Mistral comparisons
                          { source: 'Mistral Large', target: 'Sonnet 4.5', value: 76, lineStyle: { width: 2, opacity: 0.38 } },
                          { source: 'Mistral Large', target: 'GPT-4o', value: 84, lineStyle: { width: 2.2, opacity: 0.4 } },
                          { source: 'Mistral Large', target: 'Gemini 2.0', value: 91, lineStyle: { width: 2.4, opacity: 0.4 } }
                        ],
                        lineStyle: {
                          color: 'source',
                          curveness: 0.3,
                        },
                        label: {
                          show: true,
                          position: 'right',
                          formatter: '{b}',
                          color: '#b5b5b5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 11,
                          fontWeight: 600
                        },
                        emphasis: {
                          focus: 'adjacency',
                          lineStyle: {
                            width: 6,
                            opacity: 0.9
                          },
                          itemStyle: {
                            shadowBlur: 30,
                            shadowColor: 'rgba(243, 112, 33, 0.8)'
                          }
                        }
                      }
                    ]
                  }}
                  style={{ height: '600px', width: '100%' }}
                  opts={{ renderer: 'svg' }}
                />
              </div>

              <div className="grid grid-cols-3 gap-3 mt-6">
                <div className="glass border border-primary/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full bg-primary" style={{boxShadow: '0 0 8px rgba(243, 112, 33, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold text-primary">TOP COMPARISON</span>
                  </div>
                  <div className="text-xl font-bold metric-value text-primary">167</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Sonnet 4.5 ↔ GPT-4o</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#8b5cf6', boxShadow: '0 0 8px rgba(139, 92, 246, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#8b5cf6'}}>STRONG LINK</span>
                  </div>
                  <div className="text-xl font-bold metric-value" style={{color: '#8b5cf6'}}>156</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">GPT-4o ↔ Gemini 2.0</div>
                </div>

                <div className="glass border border-border/20 rounded-lg p-3">
                  <div className="flex items-center space-x-2 mb-2">
                    <div className="w-3 h-3 rounded-full" style={{background: '#10b981', boxShadow: '0 0 8px rgba(16, 185, 129, 0.6)'}} />
                    <span className="text-xs font-mono font-semibold" style={{color: '#10b981'}}>TOTAL LINKS</span>
                  </div>
                  <div className="text-xl font-bold metric-value text-green-500">18</div>
                  <div className="text-xs text-muted-foreground/70 font-sans mt-1">Comparison pairs</div>
                </div>
              </div>

              <div className="glass border border-primary/20 rounded-lg p-4 mt-4" style={{background: 'rgba(243, 112, 33, 0.05)'}}>
                <div className="text-xs font-mono text-muted-foreground tracking-wider uppercase mb-2">
                  CHORD DIAGRAM FEATURES
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-semibold">Relationship visualization</span> - Circular layout shows which models are most frequently compared</li>
                  <li>• <span className="text-primary font-semibold">Curved ribbons</span> - Ribbon thickness represents comparison frequency (thicker = more comparisons)</li>
                  <li>• <span className="text-primary font-semibold">Color-coded nodes</span> - Each model uses its provider color (Anthropic=Orange, OpenAI=Blue, etc.)</li>
                  <li>• <span className="text-primary font-semibold">Interactive exploration</span> - Hover over nodes to highlight all connected relationships</li>
                  <li>• <span className="text-primary font-semibold">Comparative insights</span> - Reveals which models users compare most (Sonnet 4.5 ↔ GPT-4o is #1)</li>
                  <li>• <span className="text-primary font-semibold">Symmetrical layout</span> - Circular arrangement with equal spacing for visual balance</li>
                  <li>• <span className="text-primary font-semibold">Pattern discovery</span> - Identify clusters of related models and cross-provider comparisons</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Graph (Node & Edge) Visualization */}
      <section className="space-y-6 animate-fade-in-up stagger-11">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">GRAPH VISUALIZATION</h2>
          <p className="text-muted-foreground font-sans">
            Force-directed network graphs showing relationships between entities with interactive node positioning and edge highlighting
          </p>
        </div>

        <Card className="glass border-border/50">
          <CardContent className="pt-6">
            <div className="space-y-6">
              <div>
                <h3 className="text-lg font-mono font-semibold mb-4">Model Dependency Network</h3>
                <div style={{ width: '100%', height: '600px' }}>
                  <ReactECharts
                    option={{
                      backgroundColor: 'transparent',
                      animation: true,
                      animationDuration: 2000,
                      animationEasing: 'elasticOut',
                      tooltip: {
                        backgroundColor: 'rgba(26, 26, 26, 0.95)',
                        borderColor: '#f37021',
                        borderWidth: 1,
                        textStyle: {
                          color: '#f5f5f5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 12,
                        },
                        formatter: function(params: any) {
                          if (params.dataType === 'node') {
                            return `<div style="padding: 4px;">
                              <div style="color: #f37021; font-weight: bold; margin-bottom: 4px;">${params.data.name}</div>
                              <div style="color: #b5b5b5;">Type: ${params.data.category}</div>
                              <div style="color: #b5b5b5;">Connections: ${params.data.value || 0}</div>
                            </div>`
                          } else {
                            return `<div style="padding: 4px;">
                              <div style="color: #f37021; font-weight: bold; margin-bottom: 4px;">${params.data.source} → ${params.data.target}</div>
                              <div style="color: #b5b5b5;">Strength: ${params.data.value}</div>
                              <div style="color: #b5b5b5;">${params.data.label || ''}</div>
                            </div>`
                          }
                        }
                      },
                      legend: {
                        data: ['LLM Models', 'Eval Systems', 'Data Sources', 'Integration Points'],
                        top: 20,
                        textStyle: {
                          color: '#b5b5b5',
                          fontFamily: 'IBM Plex Mono, monospace',
                          fontSize: 11,
                        },
                        itemStyle: {
                          borderWidth: 0,
                        },
                      },
                      series: [
                        {
                          type: 'graph',
                          layout: 'force',
                          data: [
                            // LLM Models (center of graph)
                            { name: 'Sonnet 4.5', value: 12, symbolSize: 60, category: 'LLM Models', itemStyle: { color: '#f37021', shadowBlur: 20, shadowColor: '#f3702199' } },
                            { name: 'Opus 4', value: 8, symbolSize: 50, category: 'LLM Models', itemStyle: { color: '#10b981', shadowBlur: 15, shadowColor: '#10b98199' } },
                            { name: 'GPT-4o', value: 10, symbolSize: 55, category: 'LLM Models', itemStyle: { color: '#60a5fa', shadowBlur: 18, shadowColor: '#60a5fa99' } },
                            { name: 'Gemini 2.0', value: 7, symbolSize: 48, category: 'LLM Models', itemStyle: { color: '#8b5cf6', shadowBlur: 14, shadowColor: '#8b5cf699' } },
                            { name: 'Llama 3.3', value: 6, symbolSize: 45, category: 'LLM Models', itemStyle: { color: '#ec4899', shadowBlur: 12, shadowColor: '#ec489999' } },

                            // Eval Systems
                            { name: 'Hawk Judge', value: 15, symbolSize: 65, category: 'Eval Systems', itemStyle: { color: '#fbbf24', shadowBlur: 22, shadowColor: '#fbbf2499' } },
                            { name: 'Regression Suite', value: 8, symbolSize: 50, category: 'Eval Systems', itemStyle: { color: '#fbbf24', shadowBlur: 15, shadowColor: '#fbbf2499' } },
                            { name: 'SQL Pattern Tests', value: 10, symbolSize: 55, category: 'Eval Systems', itemStyle: { color: '#fbbf24', shadowBlur: 18, shadowColor: '#fbbf2499' } },
                            { name: 'Churn Analysis', value: 6, symbolSize: 45, category: 'Eval Systems', itemStyle: { color: '#fbbf24', shadowBlur: 12, shadowColor: '#fbbf2499' } },

                            // Data Sources
                            { name: 'Loom Telemetry', value: 11, symbolSize: 58, category: 'Data Sources', itemStyle: { color: '#14b8a6', shadowBlur: 20, shadowColor: '#14b8a699' } },
                            { name: 'Promptio Library', value: 9, symbolSize: 52, category: 'Data Sources', itemStyle: { color: '#14b8a6', shadowBlur: 16, shadowColor: '#14b8a699' } },
                            { name: 'SQLite Store', value: 7, symbolSize: 48, category: 'Data Sources', itemStyle: { color: '#14b8a6', shadowBlur: 14, shadowColor: '#14b8a699' } },
                            { name: 'OpenTelemetry', value: 5, symbolSize: 42, category: 'Data Sources', itemStyle: { color: '#14b8a6', shadowBlur: 10, shadowColor: '#14b8a699' } },

                            // Integration Points
                            { name: 'gRPC API', value: 13, symbolSize: 62, category: 'Integration Points', itemStyle: { color: '#f97316', shadowBlur: 21, shadowColor: '#f9731699' } },
                            { name: 'HTTP Gateway', value: 8, symbolSize: 50, category: 'Integration Points', itemStyle: { color: '#f97316', shadowBlur: 15, shadowColor: '#f9731699' } },
                            { name: 'Web UI', value: 6, symbolSize: 45, category: 'Integration Points', itemStyle: { color: '#f97316', shadowBlur: 12, shadowColor: '#f9731699' } },
                            { name: 'CLI', value: 7, symbolSize: 48, category: 'Integration Points', itemStyle: { color: '#f97316', shadowBlur: 14, shadowColor: '#f9731699' } },
                          ],
                          links: [
                            // LLM Models → Eval Systems
                            { source: 'Sonnet 4.5', target: 'Hawk Judge', value: 95, label: 'primary judge model' },
                            { source: 'Opus 4', target: 'Hawk Judge', value: 78, label: 'fallback judge' },
                            { source: 'GPT-4o', target: 'Regression Suite', value: 82, label: 'test subject' },
                            { source: 'Gemini 2.0', target: 'SQL Pattern Tests', value: 71, label: 'test subject' },
                            { source: 'Llama 3.3', target: 'Churn Analysis', value: 64, label: 'test subject' },

                            // Eval Systems → Data Sources
                            { source: 'Hawk Judge', target: 'Loom Telemetry', value: 88, label: 'writes results' },
                            { source: 'Hawk Judge', target: 'Promptio Library', value: 92, label: 'loads prompts' },
                            { source: 'Regression Suite', target: 'SQLite Store', value: 85, label: 'stores evals' },
                            { source: 'SQL Pattern Tests', target: 'Loom Telemetry', value: 79, label: 'writes results' },
                            { source: 'Churn Analysis', target: 'OpenTelemetry', value: 67, label: 'exports traces' },

                            // Data Sources → Integration Points
                            { source: 'Loom Telemetry', target: 'gRPC API', value: 90, label: 'queries' },
                            { source: 'Promptio Library', target: 'gRPC API', value: 76, label: 'template loading' },
                            { source: 'SQLite Store', target: 'HTTP Gateway', value: 83, label: 'read operations' },
                            { source: 'OpenTelemetry', target: 'Web UI', value: 72, label: 'visualization' },

                            // Integration Points → LLM Models (feedback loop)
                            { source: 'gRPC API', target: 'Sonnet 4.5', value: 87, label: 'inference requests' },
                            { source: 'HTTP Gateway', target: 'GPT-4o', value: 74, label: 'inference requests' },
                            { source: 'Web UI', target: 'Gemini 2.0', value: 68, label: 'user queries' },
                            { source: 'CLI', target: 'Llama 3.3', value: 61, label: 'batch processing' },

                            // Cross-system connections
                            { source: 'Hawk Judge', target: 'gRPC API', value: 93, label: 'service calls' },
                            { source: 'Loom Telemetry', target: 'Promptio Library', value: 81, label: 'template metadata' },
                            { source: 'SQLite Store', target: 'OpenTelemetry', value: 70, label: 'trace export' },
                          ],
                          categories: [
                            { name: 'LLM Models' },
                            { name: 'Eval Systems' },
                            { name: 'Data Sources' },
                            { name: 'Integration Points' },
                          ],
                          roam: true,
                          draggable: true,
                          label: {
                            show: true,
                            position: 'right',
                            color: '#f5f5f5',
                            fontFamily: 'IBM Plex Mono, monospace',
                            fontSize: 11,
                            formatter: '{b}'
                          },
                          edgeLabel: {
                            show: false,
                            fontSize: 10,
                            color: '#b5b5b5',
                            fontFamily: 'IBM Plex Mono, monospace',
                          },
                          lineStyle: {
                            color: 'source',
                            curveness: 0.2,
                            width: 2,
                            opacity: 0.6,
                          },
                          emphasis: {
                            focus: 'adjacency',
                            label: {
                              show: true,
                            },
                            lineStyle: {
                              width: 4,
                              opacity: 1,
                            },
                          },
                          force: {
                            repulsion: 400,
                            gravity: 0.1,
                            edgeLength: 150,
                            layoutAnimation: true,
                          },
                        }
                      ]
                    }}
                    style={{ height: '100%', width: '100%' }}
                  />
                </div>
              </div>

              <div className="grid grid-cols-3 gap-4">
                <div className="glass rounded-lg p-4 border border-border/50">
                  <div className="text-3xl font-bold font-mono mb-2 glow-text">17</div>
                  <div className="text-sm text-muted-foreground font-sans">Total Nodes</div>
                  <div className="text-xs text-muted-foreground/70 mt-1 font-mono">
                    4 categories
                  </div>
                </div>
                <div className="glass rounded-lg p-4 border border-border/50">
                  <div className="text-3xl font-bold font-mono mb-2 glow-text">23</div>
                  <div className="text-sm text-muted-foreground font-sans">Total Edges</div>
                  <div className="text-xs text-muted-foreground/70 mt-1 font-mono">
                    cross-system
                  </div>
                </div>
                <div className="glass rounded-lg p-4 border border-border/50">
                  <div className="text-3xl font-bold font-mono mb-2 glow-text">Hawk Judge</div>
                  <div className="text-sm text-muted-foreground font-sans">Most Connected</div>
                  <div className="text-xs text-muted-foreground/70 mt-1 font-mono">
                    15 connections
                  </div>
                </div>
              </div>

              <div className="space-y-2">
                <h4 className="font-mono font-semibold text-sm">Key Features</h4>
                <ul className="space-y-1 text-sm text-muted-foreground font-sans">
                  <li>• <span className="text-primary font-mono">Force-directed layout</span> - Nodes arrange automatically based on relationships</li>
                  <li>• <span className="text-primary font-mono">Interactive positioning</span> - Drag nodes to explore different arrangements</li>
                  <li>• <span className="text-primary font-mono">Adjacency highlighting</span> - Hover over nodes to see connected nodes and edges</li>
                  <li>• <span className="text-primary font-mono">Category grouping</span> - Color-coded by entity type (models, eval systems, data sources, integrations)</li>
                  <li>• <span className="text-primary font-mono">Edge strength</span> - Line thickness represents connection strength</li>
                  <li>• <span className="text-primary font-mono">Zoom & pan</span> - Navigate large graphs with mouse wheel and drag</li>
                  <li>• <span className="text-primary font-mono">Node sizing</span> - Size indicates number of connections (network centrality)</li>
                  <li>• <span className="text-primary font-mono">Relationship labels</span> - Describes the nature of each connection</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>

      {/* Usage Guidelines */}
      <section className="space-y-6 animate-fade-in-up stagger-6">
        <div>
          <h2 className="text-3xl font-bold font-mono mb-2">USAGE GUIDELINES</h2>
          <p className="text-muted-foreground font-sans">
            Best practices for using the design system
          </p>
        </div>

        <Card className="glass border-border/50">
          <CardContent className="pt-6 space-y-6">
            <div className="grid md:grid-cols-2 gap-6">
              <div className="space-y-3">
                <div className="flex items-center space-x-2">
                  <CheckCircle className="h-5 w-5 text-primary" />
                  <h3 className="font-mono font-semibold">Do</h3>
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• Use monospace for headings, data, and labels</li>
                  <li>• Apply staggered animations to lists and grids</li>
                  <li>• Use glass morphism for elevated surfaces</li>
                  <li>• Include status dots for state indicators</li>
                  <li>• Add glow effects to active elements</li>
                  <li>• Use semantic colors consistently</li>
                </ul>
              </div>
              <div className="space-y-3">
                <div className="flex items-center space-x-2">
                  <XCircle className="h-5 w-5 text-destructive" />
                  <h3 className="font-mono font-semibold">Don't</h3>
                </div>
                <ul className="space-y-2 text-sm text-muted-foreground font-sans">
                  <li>• Mix sans-serif fonts in headings</li>
                  <li>• Use flat colors without depth</li>
                  <li>• Forget hover states on interactive elements</li>
                  <li>• Overuse animations (keep purposeful)</li>
                  <li>• Use colors without semantic meaning</li>
                  <li>• Ignore glass morphism on cards</li>
                </ul>
              </div>
            </div>
          </CardContent>
        </Card>
      </section>
    </div>
  )
}
